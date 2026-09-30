import './style.css'
import { Activity, AllowOrigin, ClearActivity, CopyToken, DenyOrigin, Providers, RotateToken, Start, State, Stop } from '../wailsjs/go/main/App'
import { EventsOn } from '../wailsjs/runtime/runtime'

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
let activity: Act[] = []
const flashes = new Map<number, number>() // finished activity id -> flash expiry (ms)
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

  <section class="activity">
    <div class="head"><h2>Activity</h2><button class="link" id="clear">Clear</button></div>
    <div class="group" id="activity"></div>
  </section>

  <footer>Closing this window stops the bridge. Minimise it to keep it running.</footer>
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
    el.innerHTML = `<div class="empty">Looking for Claude Code and Codex…</div>`
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
      return `<div class="row"><div class="grow"><div class="name">${esc(p.name)}</div><div class="meta clip" title="${esc(detail)}">${detail}</div></div>${status}</div>`
    })
    .join('')
}

let lastOrigins = ''
function renderSites() {
  const origins = state?.origins ?? []
  const key = JSON.stringify(origins)
  if (key === lastOrigins) return
  lastOrigins = key
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
      return `<div class="row">
        <span class="when">${clock(a.started_at)}</span>
        <div class="grow">
          <div class="clip"><span class="name">${esc(who)}</span> <span class="meta">${what}${tokens}</span></div>
          ${a.status === 'error' && a.error ? `<div class="err">${esc(a.error)}</div>` : ''}
        </div>
        ${status}
      </div>`
    })
    .join('')
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

$('#refresh').onclick = async () => {
  providers = []
  renderTools()
  providers = await Providers(true)
  renderAll()
}

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
  }
  renderPatch()
  renderActivity()
})

EventsOn('activity:reset', async () => {
  activity = (await Activity()) as unknown as Act[]
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
  activity = (await Activity()) as unknown as Act[]
  renderAll()
  providers = await Providers(false)
  renderAll()
}

void boot()
