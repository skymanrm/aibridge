// Capture service: imports a workflow into the private n8n and screenshots its editor with Playwright.
import http from 'node:http'
import { randomUUID } from 'node:crypto'
import { chromium } from 'playwright'

const N8N = process.env.N8N_URL ?? 'http://n8n:5678'
const EMAIL = process.env.STUDIO_EMAIL ?? 'studio@ai-bridge.local'
const PASSWORD = process.env.STUDIO_PASSWORD ?? 'AiBridge-Studio-1'
const PORT = Number(process.env.PORT ?? 7778)
const MAX_BODY = 4 << 20
const MAX_NODES = 60
const STICKY = 'n8n-nodes-base.stickyNote'

class HttpError extends Error {
  constructor(status, code, message, extra = {}) {
    super(message)
    Object.assign(this, { status, code, extra })
  }
}

/** Cookie-authenticated client for the n8n internal REST API (owner account is created on first use). */
class N8n {
  static cookie = ''
  static version = ''
  static nodes = null // type -> [{versions, entry}]
  static credentials = null // type -> credential type description

  static async request(method, path, body, retry = true) {
    const res = await fetch(N8N + path, {
      method,
      headers: { 'Content-Type': 'application/json', Cookie: this.cookie },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    if (res.status === 401 && retry) {
      await this.login()
      return this.request(method, path, body, false)
    }
    const text = await res.text()
    if (!res.ok) throw new Error(`n8n ${method} ${path}: ${res.status} ${text.slice(0, 300)}`)
    if (!text) return null
    const json = JSON.parse(text)
    return json && typeof json === 'object' && 'data' in json ? json.data : json
  }

  static async login() {
    const res = await fetch(`${N8N}/rest/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ emailOrLdapLoginId: EMAIL, password: PASSWORD }),
    })
    if (!res.ok) throw new Error(`n8n login failed: ${res.status} ${(await res.text()).slice(0, 200)}`)
    const auth = res.headers.getSetCookie().find((c) => c.startsWith('n8n-auth='))
    if (!auth) throw new Error('n8n login returned no auth cookie')
    this.cookie = auth.split(';')[0]
  }

  /** Waits for n8n, creates the owner account once, logs in and loads the node catalogue. */
  static async init() {
    for (let i = 0; ; i++) {
      try {
        const res = await fetch(`${N8N}/healthz/readiness`)
        if (res.ok) break
      } catch {}
      if (i > 120) throw new Error('n8n did not become ready')
      await new Promise((r) => setTimeout(r, 1000))
    }
    const settings = await this.request('GET', '/rest/settings', undefined, false)
    this.version = settings.versionCli ?? ''
    if (settings.userManagement?.showSetupOnFirstLoad) {
      await this.request('POST', '/rest/owner/setup', { email: EMAIL, firstName: 'AI', lastName: 'Bridge', password: PASSWORD }, false)
    }
    await this.login()
    const nodes = await this.request('GET', '/types/nodes.json')
    this.nodes = new Map()
    for (const entry of nodes) {
      const versions = [entry.version].flat()
      if (!this.nodes.has(entry.name)) this.nodes.set(entry.name, [])
      this.nodes.get(entry.name).push({ versions, entry })
    }
    const creds = await this.request('GET', '/types/credentials.json')
    this.credentials = new Map(creds.map((c) => [c.name, c]))
    if (!this.version) this.version = (await this.request('GET', '/rest/settings')).versionCli ?? ''
  }

  /** Placeholder credential per type, so nodes do not show "credentials not set" warnings. */
  static credentialCache = new Map()
  static async placeholderCredential(type) {
    if (this.credentialCache.has(type)) return this.credentialCache.get(type)
    const existing = (await this.request('GET', '/rest/credentials')) ?? []
    for (const c of existing) if (!this.credentialCache.has(c.type)) this.credentialCache.set(c.type, { id: c.id, name: c.name })
    if (this.credentialCache.has(type)) return this.credentialCache.get(type)
    const display = (this.credentials.get(type)?.displayName ?? type).replace(/\s*(OAuth2 API|OAuth2|API)$/i, '')
    const created = await this.request('POST', '/rest/credentials', { name: `${display} account`, type, data: {} })
    const ref = { id: created.id, name: created.name }
    this.credentialCache.set(type, ref)
    return ref
  }
}

/** Structural checks and fixes before import: known node types, supported versions, connections, credentials. */
class Workflow {
  static latest(versions) {
    return Math.max(...versions.filter((v) => typeof v === 'number'))
  }

  static describe(type) {
    const variants = N8n.nodes.get(type)
    return variants ? `${type} (${variants[0].entry.displayName})` : type
  }

  static suggest(type) {
    const words = type.toLowerCase().split(/[^a-z0-9]+/).filter((w) => w.length > 2 && !['n8n', 'nodes', 'base', 'langchain'].includes(w))
    const tail = type.split('.').pop().toLowerCase()
    const scored = []
    for (const [name, variants] of N8n.nodes) {
      const hay = `${name} ${variants[0].entry.displayName}`.toLowerCase()
      let score = name.split('.').pop().toLowerCase() === tail ? 10 : 0
      for (const w of words) if (hay.includes(w)) score += w.length
      if (score > 0) scored.push([score, name])
    }
    return scored.sort((a, b) => b[0] - a[0]).slice(0, 4).map(([, n]) => this.describe(n))
  }

  static entryFor(node) {
    return N8n.nodes.get(node.type)?.find((v) => v.versions.includes(node.typeVersion))?.entry
  }

  static matches(expected, actual) {
    return expected.some((e) => {
      if (e && typeof e === 'object' && e._cnd) {
        const [op, arg] = Object.entries(e._cnd)[0]
        if (op === 'gte') return actual >= arg
        if (op === 'lte') return actual <= arg
        if (op === 'gt') return actual > arg
        if (op === 'lt') return actual < arg
        if (op === 'eq') return actual === arg
        if (op === 'not') return actual !== arg
        return true
      }
      return e === actual
    })
  }

  /** Whether displayOptions show a credential for this node's parameters (defaults filled from the schema). */
  static shown(displayOptions, node, entry) {
    if (!displayOptions) return true
    const value = (key) => {
      if (key === '@version') return node.typeVersion
      if (key in node.parameters) return node.parameters[key]
      return entry.properties?.find((p) => p.name === key)?.default
    }
    for (const [key, expected] of Object.entries(displayOptions.show ?? {})) if (!this.matches(expected, value(key))) return false
    for (const [key, expected] of Object.entries(displayOptions.hide ?? {})) if (this.matches(expected, value(key))) return false
    return true
  }

  /** Returns {workflow, errors, notes}; errors are problems the author (AI) must fix. */
  static async prepare(input) {
    const errors = []
    const notes = []
    if (!input || typeof input !== 'object' || !Array.isArray(input.nodes) || !input.nodes.length) {
      return { workflow: null, errors: ['Workflow JSON must have a non-empty "nodes" array'], notes }
    }
    if (input.nodes.length > MAX_NODES) errors.push(`Too many nodes (${input.nodes.length}); keep it under ${MAX_NODES}`)
    const names = new Set()
    const nodes = []
    for (const [i, raw] of input.nodes.entries()) {
      const node = { ...raw }
      if (typeof node.name !== 'string' || !node.name.trim()) {
        errors.push(`Node #${i + 1} has no name`)
        continue
      }
      if (names.has(node.name)) errors.push(`Duplicate node name "${node.name}"`)
      names.add(node.name)
      const variants = N8n.nodes.get(node.type)
      if (!variants) {
        const hint = this.suggest(String(node.type ?? ''))
        errors.push(`Node "${node.name}": unknown type "${node.type}"` + (hint.length ? `; did you mean ${hint.join(', ')}?` : ''))
        continue
      }
      const all = variants.flatMap((v) => v.versions)
      if (!all.includes(node.typeVersion)) {
        const fixed = this.latest(all)
        if (node.typeVersion !== undefined) notes.push(`"${node.name}": typeVersion ${node.typeVersion} → ${fixed}`)
        node.typeVersion = fixed
      }
      node.id = typeof node.id === 'string' && node.id ? node.id : randomUUID()
      node.parameters = node.parameters && typeof node.parameters === 'object' ? node.parameters : {}
      if (!Array.isArray(node.position) || node.position.length !== 2 || !node.position.every(Number.isFinite)) {
        node.position = [i * 260, 0]
      }
      nodes.push(node)
    }
    const connections = input.connections && typeof input.connections === 'object' ? input.connections : {}
    for (const [from, outputs] of Object.entries(connections)) {
      if (!names.has(from)) errors.push(`Connection from unknown node "${from}" (connections are keyed by node name)`)
      for (const [kind, groups] of Object.entries(outputs ?? {})) {
        if (!Array.isArray(groups)) {
          errors.push(`Connections of "${from}".${kind} must be an array of arrays`)
          continue
        }
        for (const group of groups) {
          for (const c of group ?? []) {
            if (!c || !names.has(c.node)) errors.push(`Connection "${from}" → unknown node "${c?.node}"`)
          }
        }
      }
    }
    if (!errors.length) {
      for (const node of nodes) {
        if (node.type === STICKY) continue
        const entry = this.entryFor(node)
        const wanted = new Set(Object.keys(node.credentials ?? {}))
        for (const c of entry?.credentials ?? []) {
          if (wanted.size ? wanted.has(c.name) : c.required && this.shown(c.displayOptions, node, entry)) wanted.add(c.name)
        }
        const credentials = {}
        for (const type of wanted) {
          if (!N8n.credentials.has(type)) continue
          credentials[type] = await N8n.placeholderCredential(type)
        }
        if (Object.keys(credentials).length) node.credentials = credentials
        else delete node.credentials
      }
    }
    const name = typeof input.name === 'string' && input.name.trim() ? input.name.trim().slice(0, 128) : 'My workflow'
    return { workflow: { name, nodes, connections, settings: { executionOrder: 'v1' } }, errors, notes }
  }
}

const HIDE_EDITOR = [
  '[data-test-id="canvas-controls"]',
  '[data-test-id^="execute-workflow-button"]',
  '[data-test-id="logs-panel"]',
  '[data-test-id="ask-assistant-chat"]',
  '[data-test-id="ask-assistant-sidebar"]',
  '[data-test-id="canvas-handle-plus-wrapper"]',
  '[data-test-id="canvas-minimap"]',
  '[data-test-id="canvas-node-toolbar"]',
  '[data-test-id="node-creator-plus-button"]',
  '[data-test-id="add-sticky-button"]',
  '[data-test-id="toggle-focus-panel-button"]',
  '[data-test-id="instance-ai-canvas-action-button"]',
  '[data-test-id="command-bar-button"]',
  '[data-test-id="banner-stack"]',
  '.vue-flow__panel',
]

/** Renders an imported workflow in headless Chromium. */
class Renderer {
  static browser = null

  static async launch() {
    this.browser = await chromium.launch({ args: ['--disable-dev-shm-usage'] })
  }

  /** Zoom to fit, then zoom in while the graph fills less than half of the canvas (n8n caps fit at 100%). */
  static async fit(page) {
    const click = (id) => page.evaluate((i) => document.querySelector(`[data-test-id="${i}"]`)?.click(), id)
    await click('zoom-to-fit')
    await page.waitForTimeout(600)
    for (let i = 0; i < 4; i++) {
      const fill = await page.evaluate(() => {
        const canvas = document.querySelector('[data-test-id="canvas-wrapper"]')?.getBoundingClientRect()
        const boxes = [...document.querySelectorAll('.vue-flow__node')].map((n) => n.getBoundingClientRect())
        if (!canvas || !boxes.length) return 1
        const w = Math.max(...boxes.map((b) => b.right)) - Math.min(...boxes.map((b) => b.left))
        const h = Math.max(...boxes.map((b) => b.bottom)) - Math.min(...boxes.map((b) => b.top))
        return Math.max(w / canvas.width, h / canvas.height)
      })
      if (fill > 0.5) break
      await click('zoom-in-button')
      await page.waitForTimeout(350)
    }
  }

  /** Hover text of each node's issue marker, e.g. "Parameter 'URL' is required". */
  static async issues(page) {
    const found = []
    const nodes = page.locator('[data-test-id="canvas-node"]:has([data-test-id="node-issues"])')
    for (let i = 0; i < (await nodes.count()); i++) {
      const node = nodes.nth(i)
      const name = await node.getAttribute('data-node-name')
      let text = ''
      try {
        await node.locator('[data-test-id="node-issues"]').first().hover({ timeout: 2000 })
        const tip = page.locator('body > div:visible').filter({ hasText: /^\s*Issues/ }).last()
        await tip.waitFor({ state: 'visible', timeout: 2000 })
        text = (await tip.innerText()).replace(/^\s*Issues\s*/, '').replace(/\s+/g, ' ').trim()
      } catch {}
      found.push({ node: name, message: text || 'Node shows a warning (issues in its parameters)' })
      await page.mouse.move(1, 1)
    }
    return found
  }

  static async capture(id, { width, height, scale, theme, frame, layout }) {
    const context = await this.browser.newContext({
      viewport: { width, height },
      deviceScaleFactor: scale,
      colorScheme: theme,
      baseURL: N8N,
      locale: 'en-US',
    })
    try {
      const [name, value] = N8n.cookie.split('=')
      await context.addCookies([{ name, value: value ?? '', url: N8N }])
      await context.addInitScript((t) => localStorage.setItem('N8N_THEME', t), theme)
      const page = await context.newPage()
      await page.goto(`/workflow/${id}`)
      await page.waitForSelector('[data-test-id="canvas-node"]', { timeout: 30000 })
      await page.waitForLoadState('networkidle', { timeout: 10000 }).catch(() => {})
      const issues = await this.issues(page)
      if (layout === 'auto') {
        await page.evaluate(() => document.querySelector('[data-test-id="tidy-up-button"]')?.click())
        await page.waitForTimeout(400)
      }
      await page.addStyleTag({ content: `${HIDE_EDITOR.join(',\n')} { display: none !important; }` })
      let target = page
      if (frame === 'canvas') {
        const box = await page.locator('[data-test-id="canvas-wrapper"]').boundingBox()
        await page.setViewportSize({ width: Math.round(width * 2 - box.width), height: Math.round(height * 2 - box.height) })
        await page.addStyleTag({ content: '.tab-bar-container { display: none !important; }' })
        target = page.locator('[data-test-id="canvas-wrapper"]')
      }
      await page.waitForTimeout(300)
      await this.fit(page)
      const png = await target.screenshot({ type: 'png', animations: 'disabled' })
      return { png, issues }
    } finally {
      await context.close()
    }
  }
}

/** One capture at a time: the browser and n8n are shared. */
class Studio {
  static queue = Promise.resolve()
  static ready = null

  static options(body) {
    const int = (v, lo, hi, d) => (Number.isInteger(v) ? Math.min(hi, Math.max(lo, v)) : d)
    return {
      width: int(body.width, 480, 2400, 1280),
      height: int(body.height, 360, 2400, 720),
      scale: [1, 1.5, 2].includes(body.scale) ? body.scale : 2,
      theme: body.theme === 'dark' ? 'dark' : 'light',
      frame: body.frame === 'canvas' ? 'canvas' : 'editor',
      layout: body.layout === 'auto' ? 'auto' : 'keep',
      strict: body.strict !== false,
      keep: body.keep === true,
    }
  }

  static async capture(body) {
    await this.ready
    const opts = this.options(body)
    const { workflow, errors, notes } = await Workflow.prepare(body.workflow)
    if (errors.length) {
      throw new HttpError(422, 'invalid_workflow', 'The workflow has errors', { issues: errors.map((message) => ({ message })), notes })
    }
    const created = await N8n.request('POST', '/rest/workflows', workflow)
    try {
      const { png, issues } = await Renderer.capture(created.id, opts)
      if (issues.length && opts.strict) {
        throw new HttpError(422, 'invalid_workflow', 'Some nodes show warnings in n8n', { issues, notes })
      }
      return {
        image: { mime: 'image/png', data: png.toString('base64') },
        workflow,
        issues,
        notes,
        n8n_version: N8n.version,
        url: opts.keep ? `/workflow/${created.id}` : null,
      }
    } finally {
      if (!opts.keep) {
        await N8n.request('POST', `/rest/workflows/${created.id}/archive`).catch(() => {})
        await N8n.request('DELETE', `/rest/workflows/${created.id}`).catch((e) => console.warn(e.message))
      }
    }
  }

  static enqueue(body) {
    const run = this.queue.then(() => this.capture(body))
    this.queue = run.catch(() => {})
    return run
  }
}

class Api {
  static send(res, status, body) {
    res.writeHead(status, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify(body))
  }

  static async body(req) {
    let size = 0
    const chunks = []
    for await (const chunk of req) {
      size += chunk.length
      if (size > MAX_BODY) throw new HttpError(413, 'too_large', 'Body is too large')
      chunks.push(chunk)
    }
    try {
      return JSON.parse(Buffer.concat(chunks).toString('utf8'))
    } catch {
      throw new HttpError(400, 'validation_error', 'Invalid JSON body')
    }
  }

  static async handle(req, res) {
    // Only ai-bridge talks to this service; browsers (which always send Origin on POST) are refused.
    if (req.headers.origin) return this.send(res, 403, { error: { code: 'forbidden', message: 'Browser requests are not allowed' } })
    try {
      if (req.method === 'GET' && req.url === '/health') {
        await Promise.race([Studio.ready, new Promise((_, rej) => setTimeout(() => rej(new Error('starting')), 100))])
        return this.send(res, 200, { ok: true, n8n_version: N8n.version, nodes: N8n.nodes.size })
      }
      if (req.method === 'POST' && req.url === '/capture') {
        return this.send(res, 200, await Studio.enqueue(await this.body(req)))
      }
      return this.send(res, 404, { error: { code: 'not_found', message: 'Not found' } })
    } catch (e) {
      if (e instanceof HttpError) return this.send(res, e.status, { error: { code: e.code, message: e.message, ...e.extra } })
      console.error(e)
      return this.send(res, 503, { error: { code: 'studio_failed', message: String(e.message ?? e) } })
    }
  }
}

Studio.ready = Promise.all([N8n.init(), Renderer.launch()])
Studio.ready.then(
  () => console.log(`capture ready: n8n ${N8n.version}, ${N8n.nodes.size} node types`),
  (e) => {
    console.error('startup failed:', e)
    process.exit(1)
  },
)
http.createServer((req, res) => void Api.handle(req, res)).listen(PORT, () => console.log(`capture listening on :${PORT}`))
