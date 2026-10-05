import './style.css'
import { Activity, ActivityDetail, AllowOrigin, ClearActivity, CopyToken, DenyOrigin, IntegrationPrompt, Providers, RotateToken, Start, State, Stop, TestProvider } from '../wailsjs/go/main/App'
import { BrowserOpenURL, ClipboardSetText, EventsOn } from '../wailsjs/runtime/runtime'

if (navigator.userAgent.includes('Windows')) document.documentElement.classList.add('win')

interface BridgeState {
  running: boolean
  addr: string
  version: string
  error: string
  active: number
  max_concurrent: number
  origins: string[]
  token: string
  config_path: string
}

interface Provider {
  id: string
  name: string
  available: boolean
  version: string
  default_model: string
  models: Array<{ id: string; name: string }>
  error: string
}

interface Act {
  id: number
  origin: string
  provider: string
  model: string
  status: 'running' | 'ok' | 'error' | 'cancelled'
  error?: string
  started_at: string
  duration_ms: number
  input_tokens: number
  output_tokens: number
}

let state: BridgeState | null = null
let providers: Provider[] = []
interface Detail {
  request: any
  response: any
}

let activity: Act[] = []
let openId: number | null = null
const details = new Map<number, Detail | null>() // null: bodies no longer kept
const flashes = new Map<number, number>() // finished activity id -> flash expiry (ms)
interface TestResult {
  ok: boolean
  text: string
  model: string
  duration_ms: number
  error?: string
}
const tests = new Map<string, TestResult | 'running'>() // provider id -> last connection test

const TASK_KEY = 'integration-task'
const storage = {
  get: (k: string) => {
    try {
      return localStorage.getItem(k) ?? ''
    } catch {
      return ''
    }
  },
  set: (k: string, v: string) => {
    try {
      localStorage.setItem(k, v)
    } catch {}
  },
}
let showPrompt = false

let showToken = false
let copied = false
let confirmRotate = false

const esc = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`)
const $ = <T extends HTMLElement = HTMLElement>(sel: string) => document.querySelector<T>(sel)!

const host = (origin: string) => {
  try {
    return new URL(origin).host
  } catch {
    return origin
  }
}
const providerName = (id: string) => providers.find((p) => p.id === id)?.name ?? id
const clock = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
const seconds = (ms: number) => (ms < 60_000 ? `${(ms / 1000).toFixed(1)} s` : `${Math.floor(ms / 60_000)} m ${Math.round((ms % 60_000) / 1000)} s`)
const short = (s: string, n: number) => (s.length > n ? `${s.slice(0, n - 1)}…` : s)

function friendlyError(err: string) {
  if (/address already in use/i.test(err)) {
    const port = state?.addr.split(':')[1] ?? ''
    return `Port ${port} is already in use, probably by another copy of ai-bridge. Quit it and start again.`
  }
  return err
}

document.querySelector('#app')!.innerHTML = `
  <header class="top" id="top"></header>
  <div class="patch" id="patch" aria-hidden="true"></div>

  <section>
    <div class="head"><h2>AI tools</h2><button class="link" id="refresh">Check again</button></div>
    <div class="group" id="tools"></div>
  </section>

  <section>
    <div class="head"><h2>Websites</h2><p>Only these sites can use the bridge</p></div>
    <div class="group">
      <div id="sites"></div>
      <form class="row" id="allow-form">
        <input id="allow-input" placeholder="https://example.com" spellcheck="false" autocomplete="off" aria-label="Website address" />
        <button type="submit">Allow</button>
      </form>
    </div>
    <p class="error-line" id="allow-error" hidden></p>
  </section>

  <section>
    <div class="head"><h2>Token</h2></div>
    <div class="group"><div class="row token" id="token"></div></div>
    <p class="hint">Paste it into each website's AI settings. A new token disconnects them until you paste it again.</p>
  </section>

  <section>
    <div class="head"><h2>Connect your app</h2><p>Let your AI coding assistant do the wiring</p></div>
    <div class="group">
      <textarea id="task" rows="3" spellcheck="false" aria-label="What should the integration do?"
        placeholder="Optional: what should your app do with AI? e.g. Add a “Summarize” button to each note that streams a 3-bullet summary"></textarea>
      <div class="row">
        <div class="grow meta">Includes the API, your websites and models, but not the token.</div>
        <button class="link" id="show-prompt">Preview</button>
        <button class="go" id="copy-prompt">Copy prompt</button>
      </div>
      <pre class="prompt" id="prompt" hidden></pre>
    </div>
    <p class="hint">Paste it into Claude Code, Codex, Gemini CLI, Cursor or another assistant working in your app's project. <button class="link inline" id="guide">Integration guide</button></p>
  </section>

  <section class="activity">
    <div class="head"><h2>Activity</h2><button class="link" id="clear">Clear</button></div>
    <div class="group" id="activity"></div>
  </section>

  <footer>The bridge keeps running when you close this window. Quit it from the menu bar icon.</footer>
`

function renderTop() {
  const s = state
  const top = $('#top')
  if (!s) {
    top.innerHTML = `<h1><span class="lamp"></span>Starting…</h1>`
    return
  }
  const lamp = s.running ? 'open' : s.error ? 'err' : ''
  const title = s.running ? 'Bridge open' : 'Bridge stopped'
  const sub = s.running
    ? `Listening on <code>${esc(s.addr)}</code>, up to ${s.max_concurrent} requests at once`
    : `Websites can't reach your AI tools until you start it`
  top.innerHTML = `
    <h1><span class="lamp ${lamp}"></span>${title}</h1>
    <div class="actions">${s.running ? `<button class="big" id="toggle">Stop bridge</button>` : `<button class="big go" id="toggle">Start bridge</button>`}</div>
    <div class="sub">${sub}</div>
    ${s.error ? `<div class="alert" style="grid-column: 1 / -1"><b>Couldn't start.</b> ${esc(friendlyError(s.error))}</div>` : ''}
  `
  $('#toggle').onclick = async () => {
    state = await (s.running ? Stop() : Start())
    renderAll()
  }
}

function renderPatch() {
  const s = state
  const origins = s?.origins ?? []
  const now = Date.now()
  const running = activity.filter((a) => a.status === 'running')
  const flashing = activity.filter((a) => a.status !== 'running' && (flashes.get(a.id) ?? 0) > now)
  const wireClass = (match: (a: Act) => boolean, base: string) => {
    if (running.some(match)) return `${base} live`
    const f = flashing.find(match)
    if (f) return `${base} ${f.status === 'ok' ? 'ok' : 'fail'}`
    return base
  }

  const W = 460
  const rows = Math.max(origins.length, providers.length, 1)
  const H = Math.max(96, rows * 40 + 16)
  const cy = H / 2
  const hubX = W / 2
  const leftX = 150
  const rightX = W - 150
  const ys = (n: number) => Array.from({ length: n }, (_, i) => (n === 1 ? cy : 20 + (i * (H - 40)) / (n - 1)))
  const curve = (x1: number, y1: number, x2: number, y2: number) => {
    const mx = (x1 + x2) / 2
    return `M${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`
  }

  const parts: string[] = []
  const leftYs = ys(Math.max(origins.length, 1))
  if (origins.length === 0) {
    parts.push(`<path class="wire dim" d="${curve(leftX + 6, cy, hubX - 26, cy)}"/>`)
    parts.push(`<circle class="node" cx="${leftX}" cy="${cy}" r="5"/>`)
    parts.push(`<text class="faint" x="${leftX - 12}" y="${cy + 4}" text-anchor="end">No websites yet</text>`)
  }
  origins.forEach((o, i) => {
    const y = leftYs[i]
    const cls = wireClass((a) => a.origin === o, s?.running ? 'wire' : 'wire dim')
    parts.push(`<path class="${cls}" d="${curve(leftX + 6, y, hubX - 26, cy)}"/>`)
    parts.push(`<circle class="node on" cx="${leftX}" cy="${y}" r="5"/>`)
    parts.push(`<text x="${leftX - 12}" y="${y + 4}" text-anchor="end">${esc(short(host(o), 22))}</text>`)
  })

  const rightYs = ys(Math.max(providers.length, 1))
  providers.forEach((p, i) => {
    const y = rightYs[i]
    const cls = wireClass((a) => a.provider === p.id, s?.running && p.available ? 'wire' : 'wire dim')
    parts.push(`<path class="${cls}" d="${curve(hubX + 26, cy, rightX - 6, y)}"/>`)
    parts.push(`<circle class="node ${p.available ? 'on' : ''}" cx="${rightX}" cy="${y}" r="5"/>`)
    parts.push(`<text class="${p.available ? '' : 'faint'}" x="${rightX + 12}" y="${y + 4}">${esc(p.name)}</text>`)
  })

  const busy = running.length > 0
  parts.push(`<circle class="hub ${s?.running ? 'open' : ''}" cx="${hubX}" cy="${cy}" r="24"/>`)
  parts.push(busy
    ? `<circle class="core busy" cx="${hubX}" cy="${cy}" r="12"/><text class="hubnum" x="${hubX}" y="${cy + 4}" text-anchor="middle" style="fill:#fff">${running.length}</text>`
    : `<circle class="core ${s?.running ? 'open' : ''}" cx="${hubX}" cy="${cy}" r="9"/>`)

  $('#patch').innerHTML = `<svg viewBox="0 0 ${W} ${H}" role="img">${parts.join('')}</svg>`
}

function renderTools() {
  const el = $('#tools')
  if (!providers.length) {
    el.innerHTML = `<div class="empty">Looking for Claude Code, Codex and Gemini CLI…</div>`
    return
  }
  el.innerHTML = providers
    .map((p) => {
      const status = p.available
        ? `<span class="state ok"><span class="dot"></span>Ready</span>`
        : `<span class="state bad"><span class="dot"></span>Not available</span>`
      const detail = p.available
        ? `${esc(p.version)}, ${p.models.length} ${p.models.length === 1 ? 'model' : 'models'}, default ${esc(p.default_model)}`
        : esc(p.error || 'Not installed')
      const t = tests.get(p.id)
      const result =
        t === undefined || t === 'running'
          ? ''
          : t.ok
            ? `<div class="meta test ok clip">Answered “${esc(short(t.text, 40))}” in ${seconds(t.duration_ms)} with ${esc(t.model)}</div>`
            : `<div class="meta test bad">${esc(t.error ?? 'Failed')}</div>`
      const button = p.available
        ? `<button data-test="${esc(p.id)}" ${t === 'running' ? 'disabled' : ''}>${t === 'running' ? 'Testing…' : 'Test'}</button>`
        : ''
      return `<div class="row"><div class="grow"><div class="name">${esc(p.name)}</div><div class="meta clip" title="${esc(detail)}">${detail}</div>${result}</div>${status}${button}</div>`
    })
    .join('')
}

let lastOrigins = ''
function renderSites() {
  const origins = state?.origins ?? []
  const key = JSON.stringify(origins)
  if (key === lastOrigins) return
  lastOrigins = key
  void refreshPrompt()
  $('#sites').innerHTML = origins.length
    ? origins
        .map((o) => `<div class="row"><div class="grow clip">${esc(o)}</div><button class="link danger" data-deny="${esc(o)}">Remove</button></div>`)
        .join('')
    : ''
  $('#sites').querySelectorAll<HTMLButtonElement>('[data-deny]').forEach((b) => {
    b.onclick = async () => {
      state = await DenyOrigin(b.dataset.deny!)
      renderAll()
    }
  })
}

function renderToken() {
  const t = state?.token ?? ''
  const shown = showToken ? t : t ? `${t.slice(0, 4)}${'•'.repeat(16)}${t.slice(-4)}` : ''
  $('#token').innerHTML = `
    <code title="${showToken ? esc(t) : ''}">${esc(shown)}</code>
    <button class="link" id="reveal">${showToken ? 'Hide' : 'Show'}</button>
    <button id="copy">${copied ? 'Copied' : 'Copy'}</button>
    <button id="rotate" class="${confirmRotate ? 'danger' : ''}">${confirmRotate ? 'Replace token' : 'New token'}</button>
  `
  $('#reveal').onclick = () => {
    showToken = !showToken
    renderToken()
  }
  $('#copy').onclick = async () => {
    await CopyToken()
    copied = true
    renderToken()
    setTimeout(() => {
      copied = false
      renderToken()
    }, 1500)
  }
  $('#rotate').onclick = async () => {
    if (!confirmRotate) {
      confirmRotate = true
      renderToken()
      setTimeout(() => {
        confirmRotate = false
        renderToken()
      }, 4000)
      return
    }
    confirmRotate = false
    state = await RotateToken()
    renderAll()
  }
}

function renderActivity() {
  const el = $('#activity')
  if (!activity.length) {
    el.innerHTML = `<div class="empty">Requests from websites will show up here.</div>`
    return
  }
  el.innerHTML = activity
    .slice(0, 50)
    .map((a) => {
      const who = a.origin ? host(a.origin) : 'Terminal'
      const what = `${esc(providerName(a.provider))}, ${esc(a.model || 'default model')}`
      const tokens = a.status === 'ok' && a.output_tokens ? `, ${a.output_tokens} tokens out` : ''
      const status =
        a.status === 'running'
          ? `<span class="state run"><span class="dot"></span><span data-elapsed="${esc(a.started_at)}">${seconds(Date.now() - Date.parse(a.started_at))}</span></span>`
          : a.status === 'ok'
            ? `<span class="state ok"><span class="dot"></span>${seconds(a.duration_ms)}</span>`
            : a.status === 'cancelled'
              ? `<span class="state"><span class="dot"></span>Cancelled</span>`
              : `<span class="state bad"><span class="dot"></span>Failed</span>`
      const open = a.id === openId
      return `<div class="row act${open ? ' open' : ''}" data-act="${a.id}" role="button" tabindex="0" aria-expanded="${open}">
        <span class="when">${clock(a.started_at)}</span>
        <div class="grow">
          <div class="clip"><span class="name">${esc(who)}</span> <span class="meta">${what}${tokens}</span></div>
          ${a.status === 'error' && a.error ? `<div class="err">${esc(a.error)}</div>` : ''}
        </div>
        ${status}
        <span class="chev" aria-hidden="true">›</span>
      </div>${open ? renderDetail(a) : ''}`
    })
    .join('')
}

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)
const block = (label: string, text: string) => `<div class="label">${esc(label)}</div><pre>${esc(text)}</pre>`

function renderDetail(a: Act) {
  const d = details.get(a.id)
  if (d === undefined) return `<div class="detail"><div class="faint">Loading…</div></div>`
  if (d === null) return `<div class="detail"><div class="faint">Request and response are kept only for the 20 most recent requests.</div></div>`

  const req = d.request ?? {}
  const sent: string[] = []
  if (req.system) sent.push(block('System', req.system))
  for (const m of req.messages ?? []) sent.push(block(cap(m.role || 'message'), m.content ?? ''))
  if (req.prompt) sent.push(block('Prompt', req.prompt))
  const opts = [req.model && `model ${req.model}`, req.effort && `effort ${req.effort}`, req.size && `size ${req.size}`].filter(Boolean)

  const res = d.response
  const got: string[] = []
  if (a.status === 'running') got.push(`<div class="faint">Waiting for the answer…</div>`)
  else if (a.status === 'cancelled') got.push(`<div class="faint">Cancelled before an answer arrived.</div>`)
  else if (res?.error) got.push(`<div class="err">${esc(res.error.code)}: ${esc(res.error.message)}</div>`)
  if (res?.text) got.push(block('Text', res.text))
  for (const img of res?.images ?? []) got.push(`<img src="data:${esc(img.mime)};base64,${img.data}" alt="Generated image" />`)
  if (res?.usage) got.push(`<div class="faint">${res.usage.input_tokens} tokens in, ${res.usage.output_tokens} tokens out</div>`)

  const head = (title: string, key: string, has: boolean) =>
    `<div class="dhead"><h3>${title}</h3>${has ? `<button class="link" data-copy="${key}" data-id="${a.id}">Copy JSON</button>` : ''}</div>`
  return `<div class="detail">
    ${head('Request', 'request', true)}
    ${opts.length ? `<div class="faint">${esc(opts.join(', '))}</div>` : ''}
    ${sent.join('')}
    ${head('Response', 'response', !!res)}
    ${got.join('')}
  </div>`
}

async function loadDetail(id: number) {
  details.set(id, ((await ActivityDetail(id)) as unknown as Detail | null) ?? null)
  if (id === openId) renderActivity()
}

async function toggleActivity(id: number) {
  openId = openId === id ? null : id
  details.clear()
  renderActivity()
  if (openId !== null) await loadDetail(openId)
}

function renderAll() {
  renderTop()
  renderPatch()
  renderTools()
  renderSites()
  renderToken()
  renderActivity()
}

$('#allow-form').onsubmit = async (e) => {
  e.preventDefault()
  const input = $<HTMLInputElement>('#allow-input')
  const errEl = $('#allow-error')
  let value = input.value.trim()
  if (!value) return
  if (!/^https?:\/\//i.test(value)) value = `https://${value}`
  try {
    state = await AllowOrigin(value)
    input.value = ''
    errEl.hidden = true
    renderAll()
  } catch (err) {
    errEl.textContent = String(err)
    errEl.hidden = false
  }
}

$('#tools').addEventListener('click', async (e) => {
  const b = (e.target as HTMLElement).closest<HTMLButtonElement>('[data-test]')
  if (!b) return
  const id = b.dataset.test!
  tests.set(id, 'running')
  renderTools()
  let result: TestResult
  try {
    result = (await TestProvider(id)) as TestResult
  } catch (err) {
    result = { ok: false, text: '', model: '', duration_ms: 0, error: String(err) }
  }
  tests.set(id, result)
  renderTools()
})

const taskEl = $<HTMLTextAreaElement>('#task')
taskEl.value = storage.get(TASK_KEY)

async function refreshPrompt() {
  if (!showPrompt) return
  const el = $('#prompt')
  try {
    el.textContent = await IntegrationPrompt(taskEl.value)
  } catch (err) {
    el.textContent = String(err)
  }
}

let taskTimer = 0
taskEl.oninput = () => {
  storage.set(TASK_KEY, taskEl.value)
  clearTimeout(taskTimer)
  taskTimer = window.setTimeout(refreshPrompt, 300)
}

$('#show-prompt').onclick = async () => {
  showPrompt = !showPrompt
  $('#prompt').hidden = !showPrompt
  $('#show-prompt').textContent = showPrompt ? 'Hide' : 'Preview'
  await refreshPrompt()
}

$('#guide').onclick = () => BrowserOpenURL('https://github.com/skymanrm/aibridge/blob/main/docs/integration.md')

$('#copy-prompt').onclick = async () => {
  const btn = $('#copy-prompt')
  try {
    await ClipboardSetText(await IntegrationPrompt(taskEl.value))
    btn.textContent = 'Copied'
  } catch (err) {
    btn.textContent = 'Failed'
    btn.title = String(err)
  }
  setTimeout(() => {
    btn.textContent = 'Copy prompt'
  }, 1500)
}

$('#refresh').onclick = async () => {
  providers = []
  renderTools()
  tests.clear()
  providers = (await Providers(true)) ?? []
  renderAll()
  void refreshPrompt()
}

$('#activity').addEventListener('click', (e) => {
  const t = e.target as HTMLElement
  const copy = t.closest<HTMLElement>('[data-copy]')
  if (copy) {
    const d = details.get(Number(copy.dataset.id))
    if (d) void ClipboardSetText(JSON.stringify(d[copy.dataset.copy as keyof Detail], null, 2))
    copy.textContent = 'Copied'
    setTimeout(() => (copy.textContent = 'Copy JSON'), 1500)
    return
  }
  const row = t.closest<HTMLElement>('[data-act]')
  if (row) void toggleActivity(Number(row.dataset.act))
})

$('#activity').addEventListener('keydown', (e) => {
  const row = (e.target as HTMLElement).closest<HTMLElement>('[data-act]')
  if (row && (e.key === 'Enter' || e.key === ' ')) {
    e.preventDefault()
    void toggleActivity(Number(row.dataset.act))
  }
})

$('#clear').onclick = async () => {
  await ClearActivity()
}

EventsOn('state', (s: BridgeState) => {
  state = s
  renderTop()
  renderPatch()
  renderSites()
  renderToken()
})

EventsOn('activity', (a: Act) => {
  const i = activity.findIndex((x) => x.id === a.id)
  if (i >= 0) activity[i] = a
  else activity.unshift(a)
  if (a.status !== 'running') {
    flashes.set(a.id, Date.now() + 1600)
    setTimeout(renderPatch, 1650)
    if (a.id === openId) void loadDetail(a.id)
  }
  renderPatch()
  renderActivity()
})

EventsOn('activity:reset', async () => {
  activity = ((await Activity()) ?? []) as unknown as Act[]
  if (!activity.some((a) => a.id === openId)) openId = null
  renderAll()
})

// Keep running timers ticking without re-rendering rows.
setInterval(() => {
  document.querySelectorAll<HTMLElement>('[data-elapsed]').forEach((el) => {
    el.textContent = seconds(Date.now() - Date.parse(el.dataset.elapsed!))
  })
}, 250)

async function boot() {
  renderAll()
  state = await State()
  activity = ((await Activity()) ?? []) as unknown as Act[]
  renderAll()
  providers = (await Providers(false)) ?? []
  renderAll()
}

void boot()
