import './style.css'
import { Activity, ActivityDetail, AllowOrigin, ClearActivity, CopyToken, DenyOrigin, IntegrationPrompt, Providers, RotateToken, SetAutoStart, SetDefaultModel, SetLimit, SetPort, SetTimeout, SetTrayUsage, Start, State, Stop, TestProvider, Usage } from '../wailsjs/go/main/App'
import { BrowserOpenURL, ClipboardSetText, EventsOn } from '../wailsjs/runtime/runtime'

const isWin = navigator.userAgent.includes('Windows')
if (isWin) document.documentElement.classList.add('win')

interface BridgeState {
  running: boolean
  addr: string
  version: string
  error: string
  active: number
  max_concurrent: number
  provider_limits: Record<string, number>
  timeout_sec: number
  default_models: Record<string, string>
  tray_usage: TrayUsage
  auto_start: string[]
  origins: string[]
  token: string
  config_path: string
}

interface Provider {
  id: string
  name: string
  available: boolean
  version: string
  path: string
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

interface TrayUsage {
  style: string // off | horizontal | vertical
  window: string // session | week
  hidden?: string[]
  hide_icon?: boolean
  hide_activity?: boolean
  monochrome?: boolean
  hide_tool_icons?: boolean
  show_reset?: boolean
}

interface UsageWindow {
  id: string
  label: string
  used_percent: number
  window_minutes?: number
  resets_at?: string
}

interface UsageLimits {
  provider: string
  name: string
  supported: boolean
  plan?: string
  windows: UsageWindow[]
  fetched_at?: string
  error?: string
}

let state: BridgeState | null = null
let providers: Provider[] = []
let usage: UsageLimits[] | null = null // null until the first answer
let usageLoading = false
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

type Page = 'overview' | 'log' | 'settings'
const PAGES: Array<[Page, string]> = [['overview', 'Overview'], ['log', 'Log'], ['settings', 'Settings']]
const PAGE_KEY = 'page'
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
let page: Page = (PAGES.some(([p]) => p === storage.get(PAGE_KEY)) ? storage.get(PAGE_KEY) : 'overview') as Page

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
const limitOf = (id: string) => state?.provider_limits?.[id] || state?.max_concurrent || 2
// The chosen model applies only while the CLI still lists it, as on the bridge side.
const modelOf = (p: Provider) => {
  const chosen = state?.default_models?.[p.id]
  return chosen && p.models.some((m) => m.id === chosen) ? chosen : p.default_model
}
const runningFor = (id: string) => activity.filter((a) => a.status === 'running' && a.provider === id).length

function friendlyError(err: string) {
  if (/address already in use/i.test(err)) {
    const port = state?.addr.split(':')[1] ?? ''
    return `Port ${port} is already in use, probably by another copy of ai-bridge. Quit it and start again.`
  }
  return err
}

document.querySelector('#app')!.innerHTML = `
  <header class="top" id="top"></header>

  <main data-page="overview">
    <div class="patch" id="patch" aria-hidden="true"></div>

    <section>
      <div class="head"><h2>AI tools</h2><button class="link" id="refresh">Check again</button></div>
      <div class="group" id="tools"></div>
    </section>

    <section>
      <div class="head"><h2>Usage limits</h2><span class="side"><span class="when" id="usage-when"></span><button class="link" id="usage-refresh">Refresh</button></span></div>
      <div class="group" id="usage"></div>
    </section>

    <section class="activity">
      <div class="head"><h2>Recent requests</h2><button class="link" data-goto="log">Show all</button></div>
      <div class="group" id="recent"></div>
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

    <footer>The bridge keeps running when you close this window. Quit it from the menu bar icon.</footer>
  </main>

  <main data-page="log" hidden>
    <section class="activity">
      <div class="head"><h2>Requests <span class="count" id="log-count"></span></h2><button class="link" id="clear">Clear</button></div>
      <div class="group" id="activity"></div>
      <p class="hint">The window keeps the last 100 requests, with full request and response for the newest 20.</p>
    </section>
  </main>

  <main data-page="settings" hidden>
    <section>
      <div class="head"><h2>Requests at once</h2><p>Per AI tool; extra requests get <code>503 busy</code></p></div>
      <div class="group" id="limits"></div>
      <p class="error-line" id="limit-error" hidden></p>
    </section>

    <section>
      <div class="head"><h2>Default model</h2><p>Used when a request doesn't name a model</p></div>
      <div class="group" id="models"></div>
      <p class="error-line" id="model-error" hidden></p>
    </section>

    <section>
      <div class="head"><h2>New session at reset</h2><p>Keep the 5-hour limit rolling</p></div>
      <div class="group" id="auto-start"></div>
      <p class="error-line" id="auto-start-error" hidden></p>
      <p class="hint">When the 5-hour limit resets, the bridge sends a one-word prompt to the default model so the next window starts right away instead of at your next request. It shows up in the log as “Session start”.</p>
    </section>

    <section>
      <div class="head"><h2>${isWin ? 'Tray icon' : 'Menu bar'}</h2><p>Usage limits next to the icon</p></div>
      <div class="group" id="tray"></div>
      <p class="error-line" id="tray-error" hidden></p>
      <p class="hint">One bar per AI tool, filled by how much of the limit is used: amber from 75%, red from 90% unless monochrome. The ${isWin ? 'tooltip' : 'menu'} lists the numbers.</p>
    </section>

    <section>
      <div class="head"><h2>Timeout</h2><p>Longest a single request may run</p></div>
      <div class="group">
        <form class="row" id="timeout-form">
          <input id="timeout-input" inputmode="numeric" maxlength="4" spellcheck="false" autocomplete="off" aria-label="Timeout in seconds" />
          <span class="meta">seconds</span>
          <button type="submit" id="timeout-save" disabled>Save</button>
        </form>
      </div>
      <p class="error-line" id="timeout-error" hidden></p>
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
      <div class="head"><h2>Port</h2><p>Websites connect to <code>http://127.0.0.1:&lt;port&gt;</code></p></div>
      <div class="group">
        <form class="row" id="port-form">
          <input id="port-input" inputmode="numeric" maxlength="5" spellcheck="false" autocomplete="off" aria-label="Port" />
          <button type="submit" id="port-save" disabled>Change</button>
        </form>
      </div>
      <p class="error-line" id="port-error" hidden></p>
      <p class="hint">Changing it restarts the bridge and stops running requests. Update the address in your websites too.</p>
    </section>

    <footer id="config-path"></footer>
  </main>
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
    ? `Listening on <code>${esc(s.addr)}</code>${s.active ? `, ${s.active} ${s.active === 1 ? 'request' : 'requests'} running` : ''}`
    : `Websites can't reach your AI tools until you start it`
  top.innerHTML = `
    <h1><span class="lamp ${lamp}"></span>${title}</h1>
    <div class="actions">${s.running ? `<button class="big" id="toggle">Stop bridge</button>` : `<button class="big go" id="toggle">Start bridge</button>`}</div>
    <div class="sub">${sub}</div>
    ${s.error ? `<div class="alert" style="grid-column: 1 / -1"><b>Couldn't start.</b> ${esc(friendlyError(s.error))}</div>` : ''}
    <nav class="tabs" role="tablist">${PAGES.map(([p, label]) => {
      const badge = p === 'log' && s.active ? `<span class="badge">${s.active}</span>` : ''
      return `<button role="tab" data-goto="${p}" aria-selected="${p === page}">${label}${badge}</button>`
    }).join('')}</nav>
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
      const busy = runningFor(p.id)
      const status = p.available
        ? busy
          ? `<span class="state run"><span class="dot"></span>${busy} of ${limitOf(p.id)} running</span>`
          : `<span class="state ok"><span class="dot"></span>Ready</span>`
        : `<span class="state bad"><span class="dot"></span>Not available</span>`
      const detail = p.available
        ? `${esc(p.version)}, ${p.models.length} ${p.models.length === 1 ? 'model' : 'models'}, default ${esc(modelOf(p))}`
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
      return `<div class="row"><div class="grow"><div class="name">${esc(p.name)}</div><div class="meta clip" title="${esc(p.path || detail)}">${detail}</div>${p.path ? `<div class="meta path clip" title="${esc(p.path)}">${esc(p.path)}</div>` : ''}${result}</div>${status}${button}</div>`
    })
    .join('')
}

// "resets in 2 h 5 m" within a day, otherwise "resets Wed 06:00".
function resetsIn(iso?: string) {
  if (!iso) return ''
  const ms = Date.parse(iso) - Date.now()
  if (ms <= 0) return 'resets now'
  const mins = Math.ceil(ms / 60_000)
  if (mins < 60) return `resets in ${mins} m`
  if (mins < 24 * 60) return `resets in ${Math.floor(mins / 60)} h ${mins % 60} m`
  return `resets ${new Date(iso).toLocaleString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' })}`
}

const usageLevel = (pct: number) => (pct >= 90 ? 'high' : pct >= 75 ? 'warn' : 'ok')

function renderUsage() {
  const el = $('#usage')
  const btn = $<HTMLButtonElement>('#usage-refresh')
  btn.disabled = usageLoading
  btn.textContent = usageLoading ? 'Checking…' : 'Refresh'
  const fetched = usage?.map((u) => u.fetched_at).filter(Boolean).sort().pop()
  $('#usage-when').textContent = fetched ? `as of ${clock(fetched)}` : ''
  if (usage === null) {
    el.innerHTML = `<div class="empty">Asking the AI tools for their limits…</div>`
    return
  }
  // Tools that are missing or don't report limits (Gemini CLI) stay out of the list.
  const shown = usage.filter((u) => u.supported && (u.windows.length || !/is not available/.test(u.error ?? '')))
  if (!shown.length) {
    el.innerHTML = `<div class="empty">None of the detected AI tools reports usage limits.</div>`
    return
  }
  el.innerHTML = shown
    .map((u) => {
      const meters = u.windows
        .map((w) => {
          const pct = Math.max(0, Math.min(100, w.used_percent))
          return `<div class="meter">
            <div class="mhead"><span class="clip">${esc(w.label)}</span><span class="num">${Math.round(w.used_percent)}% used${w.resets_at ? ` · ${resetsIn(w.resets_at)}` : ''}</span></div>
            <div class="bar" role="meter" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${pct}" aria-label="${esc(`${u.name} ${w.label}`)}"><span class="${usageLevel(pct)}" style="width:${pct}%"></span></div>
          </div>`
        })
        .join('')
      const err = u.error ? `<div class="meta test bad">${esc(u.error)}</div>` : ''
      return `<div class="row usage"><div class="grow"><div class="name">${esc(u.name)}${u.plan ? ` <span class="meta">${esc(u.plan)}</span>` : ''}</div>${meters}${err}</div></div>`
    })
    .join('')
}

const TRAY_STYLES: Array<[string, string]> = [['off', 'Off'], ['horizontal', 'Horizontal'], ['vertical', 'Vertical']]
const trayOf = (): TrayUsage => state?.tray_usage ?? { style: 'horizontal', window: 'session' }

// Compact time to reset, as next to the menu bar bars: "45m", "2h" or "3d".
function resetShort(iso?: string) {
  if (!iso) return ''
  const mins = (Date.parse(iso) - Date.now()) / 60_000
  if (mins < 1) return '0m'
  if (mins < 60) return `${Math.ceil(mins)}m`
  if (mins < 24 * 60) return `${Math.max(1, Math.round(mins / 60))}h`
  return `${Math.round(mins / (24 * 60))}d`
}

const SPARKLE = 'M8 1.5 C8.6 5.6 10.4 7.4 14.5 8 C10.4 8.6 8.6 10.4 8 14.5 C7.4 10.4 5.6 8.6 1.5 8 C5.6 7.4 7.4 5.6 8 1.5 Z'

// Tiny tool marks, as in the menu bar: Claude a starburst, Codex a hexagon ring, Gemini a four-point star.
function toolMark(id: string, cx: number, cy: number, g: number, cls: string) {
  const s = g / 2
  if (id === 'claude') {
    const rays = [0, 1, 2, 3].map((i) => {
      const a = (i * Math.PI) / 4
      const dx = Math.cos(a) * s * 0.88
      const dy = Math.sin(a) * s * 0.88
      return `M${cx - dx} ${cy - dy}L${cx + dx} ${cy + dy}`
    })
    return `<path class="mark stroke ${cls}" d="${rays.join('')}" stroke-width="${Math.max(0.9, s * 0.36)}"/>`
  }
  if (id === 'codex') {
    const pts = [0, 1, 2, 3, 4, 5].map((i) => {
      const a = Math.PI / 6 + (i * Math.PI) / 3
      return `${cx + Math.cos(a) * s * 0.78},${cy + Math.sin(a) * s * 0.78}`
    })
    return `<polygon class="mark stroke ${cls}" points="${pts.join(' ')}" stroke-width="${Math.max(0.9, s * 0.34)}"/>`
  }
  if (id === 'gemini') {
    const k = s * 0.14
    return `<path class="mark ${cls}" d="M${cx} ${cy - s}C${cx + k} ${cy - k} ${cx + k} ${cy - k} ${cx + s} ${cy}C${cx + k} ${cy + k} ${cx + k} ${cy + k} ${cx} ${cy + s}C${cx - k} ${cy + k} ${cx - k} ${cy + k} ${cx - s} ${cy}C${cx - k} ${cy - k} ${cx - k} ${cy - k} ${cx} ${cy - s}Z"/>`
  }
  return `<circle class="mark ${cls}" cx="${cx}" cy="${cy}" r="${s * 0.7}"/>`
}

// Same layout as the native menu bar icon (tray_darwin.m): optional sparkle, then per AI tool its mark, bar and time to reset.
function trayPreview(t: TrayUsage) {
  const shown = (usage ?? [])
    .filter((u) => !(t.hidden ?? []).includes(u.provider))
    .map((u) => ({ id: u.provider, w: u.windows.find((w) => w.id === t.window) }))
    .filter((x): x is { id: string; w: UsageWindow } => !!x.w)
  const n = t.style === 'off' ? 0 : shown.length
  const vertical = t.style === 'vertical'
  const mono = !!t.monochrome
  const icon = !t.hide_icon || n === 0
  const tools = !t.hide_tool_icons
  const H = 20
  const area = 16
  const y0 = (H - area) / 2
  const gap = 2
  const x0 = icon ? 20 : 0
  const font = n === 1 ? 10 : n === 2 || vertical ? 8 : 6.5
  const labels = shown.slice(0, n).map(({ w }) => (t.show_reset ? resetShort(w.resets_at) : ''))
  const labelW = Math.max(0, ...labels.map((l) => l.length * font * 0.62))
  const labelGap = labelW > 0 ? 2.5 : 0
  const level = (pct: number) => (mono ? 'ok' : usageLevel(pct))
  const parts: string[] = []
  const bar = (x: number, y: number, w: number, h: number, pct: number, busy: boolean) => {
    const r = (vertical ? w : h) / 2
    parts.push(`<rect class="track" x="${x}" y="${y}" width="${w}" height="${h}" rx="${r}"/>`)
    const fw = vertical ? w : (w * pct) / 100
    const fh = vertical ? (h * pct) / 100 : h
    // Clipped to the track, so low values read as a sliver of the pill, not a thin line.
    const clip = `tc${parts.length}`
    if (fw > 0 && fh > 0)
      parts.push(`<clipPath id="${clip}"><rect x="${x}" y="${y}" width="${w}" height="${h}" rx="${r}"/></clipPath>`,
        `<rect class="lvl-${level(pct)}${busy ? ' pulse' : ''}" clip-path="url(#${clip})" x="${x}" y="${y + h - fh}" width="${fw}" height="${fh}"/>`)
  }
  const text = (x: number, cy: number, label: string) =>
    label && parts.push(`<text class="reset" x="${x}" y="${cy}" font-size="${font}" dominant-baseline="central">${label}</text>`)
  let width: number
  if (vertical) {
    const thick = n === 1 ? 6 : n === 2 ? 5 : 4
    const g = tools ? (n === 1 ? 8 : 6) : 0
    const col = Math.max(thick, g)
    const group = col + labelGap + labelW
    const groupGap = labelW > 0 ? 4 : gap
    width = x0 + n * group + (n - 1) * groupGap
    shown.slice(0, n).forEach(({ id, w }, i) => {
      const pct = Math.max(0, Math.min(100, w.used_percent))
      const busy = !t.hide_activity && runningFor(id) > 0
      const x = x0 + i * (group + groupGap)
      const barH = area - (g > 0 ? g + 1.5 : 0)
      bar(x + (col - thick) / 2, H - y0 - barH, thick, barH, pct, busy && !g)
      if (g) parts.push(toolMark(id, x + col / 2, y0 + g / 2, g, `t-${id}${mono ? ' mono' : ''}${busy ? ' pulse busy' : ''}`))
      text(x + col + labelGap, H / 2, labels[i])
    })
  } else {
    const thick = n === 1 ? 8 : n === 2 ? 6 : 4
    const g = tools ? (n === 1 ? 10 : n === 2 ? 7.5 : 5.5) : 0
    const barW = n === 1 ? 26 : 22
    const barX = x0 + (g > 0 ? g + 2 : 0)
    const stack = n * thick + (n - 1) * gap
    width = barX + barW + labelGap + labelW
    shown.slice(0, n).forEach(({ id, w }, i) => {
      const pct = Math.max(0, Math.min(100, w.used_percent))
      const busy = !t.hide_activity && runningFor(id) > 0
      const y = y0 + (area - stack) / 2 + i * (thick + gap)
      const cy = y + thick / 2
      if (g) parts.push(toolMark(id, x0 + g / 2, cy, g, `t-${id}${mono ? ' mono' : ''}${busy ? ' pulse busy' : ''}`))
      bar(barX, y, barW, thick, pct, busy && !g)
      text(barX + barW + labelGap, cy, labels[i])
    })
  }
  if (!n) width = 16
  const sparkle = icon ? `<path class="glyph" d="${SPARKLE}" transform="translate(0 2)"/>` : ''
  return `<svg class="tray-preview" viewBox="-1 0 ${width + 2} ${H}" width="${(width + 2) * 1.5}" height="${H * 1.5}" role="img" aria-label="Preview">${sparkle}${parts.join('')}</svg>`
}

let lastTray = ''
function renderTray() {
  const t = trayOf()
  const reporting = (usage ?? []).filter((u) => u.supported && u.windows.length)
  // Re-rendering on every state event would close an open dropdown.
  const running = reporting.map((u) => runningFor(u.provider))
  const key = JSON.stringify([t, running, reporting.map((u) => [u.provider, u.name, u.windows.map((w) => [w.id, w.used_percent])])])
  if (key === lastTray) return
  lastTray = key
  const seg = TRAY_STYLES.map(([v, label]) => `<button type="button" data-tray-style="${v}" aria-pressed="${t.style === v}">${label}</button>`).join('')
  const tools = usage === null
    ? `<div class="row"><div class="grow meta">Asking the AI tools for their limits…</div></div>`
    : reporting.length
      ? reporting
          .map((u) => `<label class="row"><div class="grow name">${esc(u.name)}</div>
            <input type="checkbox" class="switch" data-tray-tool="${esc(u.provider)}" ${(t.hidden ?? []).includes(u.provider) ? '' : 'checked'} ${t.style === 'off' ? 'disabled' : ''} /></label>`)
          .join('')
      : `<div class="row"><div class="grow meta">No AI tool reports usage limits.</div></div>`
  $('#tray').innerHTML = `
    <div class="row"><div class="grow name">Bars</div><div class="seg" role="group" aria-label="Bars">${seg}</div></div>
    <div class="row"><div class="grow name">Limit</div>
      <select id="tray-window" aria-label="Limit shown" ${t.style === 'off' ? 'disabled' : ''}>
        <option value="session" ${t.window === 'session' ? 'selected' : ''}>5-hour</option>
        <option value="week" ${t.window === 'week' ? 'selected' : ''}>Weekly</option>
      </select></div>
    ${tools}
    ${isWin ? '' : `<label class="row"><div class="grow"><div class="name">Show icon</div><div class="meta">Turn off to show only the bars; it stays while there are no bars</div></div>
      <input type="checkbox" class="switch" id="tray-icon" ${t.hide_icon ? '' : 'checked'} ${t.style === 'off' ? 'disabled' : ''} /></label>`}
    <label class="row"><div class="grow"><div class="name">Tool icons</div><div class="meta">A tiny Claude, Codex or Gemini mark by each bar</div></div>
      <input type="checkbox" class="switch" id="tray-tools" ${t.hide_tool_icons ? '' : 'checked'} ${t.style === 'off' ? 'disabled' : ''} /></label>
    ${isWin ? '' : `<label class="row"><div class="grow"><div class="name">Time to reset</div><div class="meta">After each bar, e.g. 45m, 2h</div></div>
      <input type="checkbox" class="switch" id="tray-reset" ${t.show_reset ? 'checked' : ''} ${t.style === 'off' ? 'disabled' : ''} /></label>`}
    <label class="row"><div class="grow"><div class="name">Activity</div><div class="meta">A tool's mark (or its bar, without icons) pulses while it runs a request</div></div>
      <input type="checkbox" class="switch" id="tray-activity" ${t.hide_activity ? '' : 'checked'} ${t.style === 'off' ? 'disabled' : ''} /></label>
    <label class="row"><div class="grow"><div class="name">Monochrome</div><div class="meta">${isWin ? 'White bars and marks' : 'In the menu bar color'}, without warning or tool colors</div></div>
      <input type="checkbox" class="switch" id="tray-mono" ${t.monochrome ? 'checked' : ''} ${t.style === 'off' ? 'disabled' : ''} /></label>
    <div class="row"><div class="grow meta">Preview</div><div class="menubar">${trayPreview(t)}</div></div>`
}

function renderAutoStart() {
  const on = state?.auto_start ?? []
  const tools = (usage ?? []).filter((u) => u.supported && u.windows.some((w) => w.id === 'session'))
  $('#auto-start').innerHTML = usage === null
    ? `<div class="empty">Asking the AI tools for their limits…</div>`
    : tools.length
      ? tools
          .map((u) => {
            const reset = u.windows.find((w) => w.id === 'session')?.resets_at
            const meta = !on.includes(u.provider) ? '' : reset && Date.parse(reset) > Date.now() ? `Next start at ${new Date(reset).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}` : 'Starts when the limits allow'
            return `<label class="row"><div class="grow"><div class="name">${esc(u.name)}</div>${meta ? `<div class="meta">${meta}</div>` : ''}</div>
              <input type="checkbox" class="switch" data-auto-start="${esc(u.provider)}" ${on.includes(u.provider) ? 'checked' : ''} /></label>`
          })
          .join('')
      : `<div class="empty">No AI tool reports a 5-hour limit.</div>`
}

async function saveTray(change: Partial<TrayUsage>) {
  const errEl = $('#tray-error')
  try {
    state = await SetTrayUsage({ ...trayOf(), ...change } as any)
    errEl.hidden = true
  } catch (err) {
    errEl.textContent = String(err)
    errEl.hidden = false
  }
  lastTray = ''
  renderTray()
}

async function loadUsage(refresh: boolean) {
  if (usageLoading) return
  usageLoading = true
  renderUsage()
  try {
    usage = ((await Usage(refresh)) ?? []) as unknown as UsageLimits[]
  } catch (err) {
    usage = usage ?? []
  } finally {
    usageLoading = false
    renderUsage()
    renderTray()
    renderAutoStart()
  }
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

const currentPort = () => state?.addr.split(':')[1] ?? ''
let lastPort = ''
function renderPort() {
  const port = currentPort()
  const input = $<HTMLInputElement>('#port-input')
  if (port !== lastPort && document.activeElement !== input) {
    lastPort = port
    input.value = port
  }
  $<HTMLButtonElement>('#port-save').disabled = !input.value.trim() || input.value.trim() === port
}

function renderLimits() {
  const el = $('#limits')
  if (!providers.length) {
    el.innerHTML = `<div class="empty">Looking for AI tools…</div>`
    return
  }
  el.innerHTML = providers
    .map((p) => {
      const n = limitOf(p.id)
      return `<div class="row"><div class="grow"><div class="name">${esc(p.name)}</div>${p.available ? '' : `<div class="meta">Not available</div>`}</div>
        <div class="stepper" role="group" aria-label="${esc(p.name)} requests at once">
          <button data-limit="${esc(p.id)}" data-delta="-1" ${n <= 1 ? 'disabled' : ''} aria-label="Fewer">−</button>
          <span class="num">${n}</span>
          <button data-limit="${esc(p.id)}" data-delta="1" ${n >= 16 ? 'disabled' : ''} aria-label="More">+</button>
        </div></div>`
    })
    .join('')
}

let lastModels = ''
function renderModels() {
  const el = $('#models')
  const listed = providers.filter((p) => p.models.length)
  // State events arrive with every request; re-rendering would close an open dropdown.
  const key = JSON.stringify([providers.length, listed.map((p) => [p.id, p.available, p.default_model, p.models]), state?.default_models])
  if (key === lastModels) return
  lastModels = key
  if (!listed.length) {
    el.innerHTML = `<div class="empty">${providers.length ? 'No AI tool reports its models' : 'Looking for AI tools…'}</div>`
    return
  }
  el.innerHTML = listed
    .map((p) => {
      const current = modelOf(p)
      const options = p.models
        .map((m) => {
          const label = m.id === p.default_model ? `${m.name || m.id} (CLI default)` : m.name || m.id
          return `<option value="${esc(m.id)}" ${m.id === current ? 'selected' : ''}>${esc(label)}</option>`
        })
        .join('')
      return `<div class="row"><div class="grow"><div class="name">${esc(p.name)}</div>${p.available ? '' : `<div class="meta">Not available</div>`}</div>
        <select data-model="${esc(p.id)}" aria-label="${esc(p.name)} default model">${options}</select></div>`
    })
    .join('')
}

let lastTimeout = ''
function renderTimeout() {
  const value = String(state?.timeout_sec ?? '')
  const input = $<HTMLInputElement>('#timeout-input')
  if (value !== lastTimeout && document.activeElement !== input) {
    lastTimeout = value
    input.value = value
  }
  $<HTMLButtonElement>('#timeout-save').disabled = !input.value.trim() || input.value.trim() === value
}

function renderPage() {
  document.querySelectorAll<HTMLElement>('main[data-page]').forEach((m) => (m.hidden = m.dataset.page !== page))
  $('#config-path').innerHTML = state?.config_path ? `Saved in <code>${esc(state.config_path)}</code>` : ''
}

function goto(p: Page) {
  page = p
  storage.set(PAGE_KEY, p)
  $('#app').scrollTop = 0
  renderTop()
  renderPage()
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

function actRow(a: Act, expandable: boolean) {
  const who = a.origin === 'auto-start' ? 'Session start' : a.origin ? host(a.origin) : 'Terminal'
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
  const open = expandable && a.id === openId
  return `<div class="row act${open ? ' open' : ''}" data-act="${a.id}" role="button" tabindex="0"${expandable ? ` aria-expanded="${open}"` : ''}>
    <span class="when">${clock(a.started_at)}</span>
    <div class="grow">
      <div class="clip"><span class="name">${esc(who)}</span> <span class="meta">${what}${tokens}</span></div>
      ${a.status === 'error' && a.error ? `<div class="err">${esc(a.error)}</div>` : ''}
    </div>
    ${status}
    <span class="chev" aria-hidden="true">›</span>
  </div>${open ? renderDetail(a) : ''}`
}

function renderActivity() {
  const recent = $('#recent')
  recent.innerHTML = activity.length
    ? activity.slice(0, 5).map((a) => actRow(a, false)).join('')
    : `<div class="empty">Requests from websites will show up here.</div>`

  const n = activity.length
  $('#log-count').textContent = n ? String(n) : ''
  const el = $('#activity')
  el.innerHTML = n
    ? activity.map((a) => actRow(a, true)).join('')
    : `<div class="empty">Requests from websites will show up here.</div>`
}

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)
const block = (label: string, text: string) => `<div class="label">${esc(label)}</div><pre>${esc(text)}</pre>`

const fmtSize = (n: number) => (n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(1)} KB` : `${(n / (1 << 20)).toFixed(1)} MB`)

function fileBlock(f: { name: string; mime: string; kind: string; size: number; data?: string; text?: string }) {
  const label = `<div class="label">File · ${esc(f.name || 'unnamed')} · ${esc(f.mime)} · ${fmtSize(f.size)}</div>`
  if (f.kind === 'image' && f.data) return `${label}<img class="attached" src="data:${esc(f.mime)};base64,${f.data}" alt="${esc(f.name)}" />`
  if (f.kind === 'text' && f.text != null) return `${label}<pre>${esc(f.text)}</pre>`
  return label
}

function renderDetail(a: Act) {
  const d = details.get(a.id)
  if (d === undefined) return `<div class="detail"><div class="faint">Loading…</div></div>`
  if (d === null) return `<div class="detail"><div class="faint">Request and response are kept only for the 20 most recent requests.</div></div>`

  const req = d.request ?? {}
  const sent: string[] = []
  if (req.system) sent.push(block('System', req.system))
  for (const m of req.messages ?? []) sent.push(block(cap(m.role || 'message'), m.content ?? ''))
  if (req.prompt) sent.push(block('Prompt', req.prompt))
  for (const f of req.files ?? []) sent.push(fileBlock(f))
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
  renderPage()
  renderPatch()
  renderTools()
  renderUsage()
  renderTray()
  renderAutoStart()
  renderModels()
  renderLimits()
  renderTimeout()
  renderSites()
  renderPort()
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

$('#port-input').oninput = () => {
  $('#port-error').hidden = true
  renderPort()
}

$('#port-form').onsubmit = async (e) => {
  e.preventDefault()
  const input = $<HTMLInputElement>('#port-input')
  const errEl = $('#port-error')
  const value = input.value.trim()
  const port = Number(value)
  if (!/^\d+$/.test(value) || port < 1 || port > 65535) {
    errEl.textContent = 'Enter a port between 1 and 65535.'
    errEl.hidden = false
    return
  }
  try {
    state = await SetPort(port)
    errEl.hidden = true
    input.blur()
    lastPort = ''
    renderAll()
    void refreshPrompt()
  } catch (err) {
    errEl.textContent = /address already in use/i.test(String(err)) ? `Port ${port} is already in use. Pick another one.` : String(err)
    errEl.hidden = false
  }
}

$('#app').addEventListener('click', (e) => {
  const b = (e.target as HTMLElement).closest<HTMLElement>('[data-goto]')
  if (b) goto(b.dataset.goto as Page)
})

$('#recent').addEventListener('click', (e) => {
  const row = (e.target as HTMLElement).closest<HTMLElement>('[data-act]')
  if (!row) return
  goto('log')
  openId = null
  void toggleActivity(Number(row.dataset.act))
})

$('#recent').addEventListener('keydown', (e) => {
  const row = (e.target as HTMLElement).closest<HTMLElement>('[data-act]')
  if (row && (e.key === 'Enter' || e.key === ' ')) {
    e.preventDefault()
    row.click()
  }
})

$('#limits').addEventListener('click', async (e) => {
  const b = (e.target as HTMLElement).closest<HTMLButtonElement>('[data-limit]')
  if (!b) return
  const id = b.dataset.limit!
  const errEl = $('#limit-error')
  try {
    state = await SetLimit(id, limitOf(id) + Number(b.dataset.delta))
    errEl.hidden = true
    renderLimits()
    renderTools()
  } catch (err) {
    errEl.textContent = String(err)
    errEl.hidden = false
  }
})

$('#models').addEventListener('change', async (e) => {
  const sel = (e.target as HTMLElement).closest<HTMLSelectElement>('[data-model]')
  if (!sel) return
  const p = providers.find((x) => x.id === sel.dataset.model)
  if (!p) return
  const errEl = $('#model-error')
  try {
    // Picking the CLI's own default clears the override, so it follows the CLI if that default changes.
    state = await SetDefaultModel(p.id, sel.value === p.default_model ? '' : sel.value)
    errEl.hidden = true
    tests.delete(p.id)
    renderModels()
    renderTools()
    void refreshPrompt()
  } catch (err) {
    errEl.textContent = String(err)
    errEl.hidden = false
    lastModels = ''
    renderModels()
  }
})

$('#timeout-input').oninput = () => {
  $('#timeout-error').hidden = true
  renderTimeout()
}

$('#timeout-form').onsubmit = async (e) => {
  e.preventDefault()
  const input = $<HTMLInputElement>('#timeout-input')
  const errEl = $('#timeout-error')
  const value = input.value.trim()
  if (!/^\d+$/.test(value)) {
    errEl.textContent = 'Enter a number of seconds.'
    errEl.hidden = false
    return
  }
  try {
    state = await SetTimeout(Number(value))
    errEl.hidden = true
    input.blur()
    lastTimeout = ''
    renderTimeout()
    void refreshPrompt()
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
  void loadUsage(true)
}

$('#usage-refresh').onclick = () => void loadUsage(true)

$('#auto-start').addEventListener('change', async (e) => {
  const el = (e.target as HTMLElement).closest<HTMLInputElement>('[data-auto-start]')
  if (!el) return
  const errEl = $('#auto-start-error')
  try {
    state = await SetAutoStart(el.dataset.autoStart!, el.checked)
    errEl.hidden = true
  } catch (err) {
    errEl.textContent = String(err)
    errEl.hidden = false
  }
  renderAutoStart()
})

$('#tray').addEventListener('click', (e) => {
  const b = (e.target as HTMLElement).closest<HTMLButtonElement>('[data-tray-style]')
  if (b) void saveTray({ style: b.dataset.trayStyle })
})

$('#tray').addEventListener('change', (e) => {
  const el = e.target as HTMLElement
  if (el.id === 'tray-window') void saveTray({ window: (el as HTMLSelectElement).value })
  if (el.id === 'tray-icon') void saveTray({ hide_icon: !(el as HTMLInputElement).checked })
  if (el.id === 'tray-activity') void saveTray({ hide_activity: !(el as HTMLInputElement).checked })
  if (el.id === 'tray-mono') void saveTray({ monochrome: (el as HTMLInputElement).checked })
  if (el.id === 'tray-tools') void saveTray({ hide_tool_icons: !(el as HTMLInputElement).checked })
  if (el.id === 'tray-reset') void saveTray({ show_reset: (el as HTMLInputElement).checked })
  const tool = el.closest<HTMLInputElement>('[data-tray-tool]')
  if (tool) {
    const hidden = new Set(trayOf().hidden ?? [])
    if (tool.checked) hidden.delete(tool.dataset.trayTool!)
    else hidden.add(tool.dataset.trayTool!)
    void saveTray({ hidden: [...hidden] })
  }
})

// The app refreshes usage itself (after requests and for the tray) and pushes it here.
EventsOn('usage', (u: UsageLimits[]) => {
  usage = u ?? []
  renderUsage()
  renderTray()
  renderAutoStart()
})

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
  renderPage()
  renderPatch()
  renderTools()
  renderTray()
  renderAutoStart()
  renderModels()
  renderLimits()
  renderTimeout()
  renderSites()
  renderPort()
  renderToken()
})

const byNewest = (x: Act, y: Act) => Date.parse(y.started_at) - Date.parse(x.started_at) || y.id - x.id

EventsOn('activity', (a: Act) => {
  const i = activity.findIndex((x) => x.id === a.id)
  if (i >= 0) activity[i] = a
  else {
    activity.unshift(a)
    activity.sort(byNewest)
  }
  if (a.status !== 'running') {
    flashes.set(a.id, Date.now() + 1600)
    setTimeout(renderPatch, 1650)
    if (a.id === openId) void loadDetail(a.id)
  }
  renderPatch()
  renderTools()
  renderTray()
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

// Keep "resets in …" current, and re-read the limits every 5 minutes while the window is shown.
setInterval(renderUsage, 30_000)
setInterval(() => {
  if (!document.hidden) void loadUsage(false)
}, 5 * 60_000)

async function boot() {
  renderAll()
  state = await State()
  activity = ((await Activity()) ?? []) as unknown as Act[]
  renderAll()
  providers = (await Providers(false)) ?? []
  renderAll()
  await loadUsage(false)
}

void boot()
