# Integrating AI Bridge into your web app

AI Bridge lets a web page call the AI CLIs installed on the visitor's own computer (Claude Code, Codex, Gemini CLI)
through `http://127.0.0.1:7777`. Your app needs no API keys and no backend for AI features. Each user runs AI Bridge
and pastes their token into your app.

```
your web app (browser) ──fetch + SSE──> 127.0.0.1:7777 (AI Bridge) ──> claude / codex / gemini
```

## Contents

- [Let your AI assistant do it](#let-your-ai-assistant-do-it)
- [Setup checklist](#setup-checklist)
- [Authentication and origins](#authentication-and-origins)
- [Endpoints](#endpoints)
- [Streaming format](#streaming-format)
- [Errors](#errors)
- [JavaScript client](#javascript-client)
- [Recipes](#recipes)
- [Providers](#providers)
- [Limits and behaviour](#limits-and-behaviour)
- [Troubleshooting](#troubleshooting)

## Let your AI assistant do it

The quickest way to integrate is to have an AI coding assistant write the code:

1. In the AI Bridge window, open **Connect your app**.
2. Optionally describe what you want, e.g. *"Add a Summarize button to each note that streams a 3-bullet summary"*.
3. Click **Copy prompt** and paste it into Claude Code, Codex, Gemini CLI, Cursor or another assistant working in your
   app's repository.

The prompt explains the API and the security rules, includes a reference client, and lists your allowed websites and the
models detected on your machine. It never contains your token. From a terminal:

```sh
ai-bridge prompt "Add a Summarize button to each note" | pbcopy   # macOS
ai-bridge prompt "Add a Summarize button to each note" | clip     # Windows
```

## Setup checklist

1. Install and log in to at least one CLI (`claude`, `codex` or `gemini`), then click **Test** next to it in the
   AI Bridge window (or run `ai-bridge test`) to confirm it answers.
2. Allow your app's origin, including the dev server: **Websites** → `http://localhost:5173`, or
   `ai-bridge allow http://localhost:5173`.
3. Copy the token (**Copy** in the window, or `ai-bridge token`) and paste it into your app's settings.

## Authentication and origins

- **Token.** Every endpoint except `GET /health` requires `Authorization: Bearer <token>`. The token is personal to
  each user's machine. Never hardcode it, commit it or send it to your server. Ask the user for it in a settings field
  and keep it in `localStorage`. **New token** in the app (or `ai-bridge token --rotate`) revokes the old one at once.
- **Origins.** Browser requests must carry an allowlisted `Origin`. Others get `403 origin_not_allowed`. Origins are
  `scheme://host[:port]` with no path, so `https://app.example.com` and `http://localhost:5173` are different entries.
  Requests without an `Origin` header (curl, scripts) skip this check but still need the token.
- **Host.** Only `127.0.0.1`, `localhost` and `[::1]` are served, which blocks DNS rebinding.
- **CORS and Private Network Access.** The bridge answers preflights for `GET, POST` with `Authorization` and
  `Content-Type`, and sets `Access-Control-Allow-Private-Network` when asked. Chromium may show the user a one-time
  prompt to let your site reach apps on their device.

## Endpoints

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/health` | none | `{name: "ai-bridge", version, authorized}`. `authorized` is `true` when the sent token is valid |
| GET | `/v1/providers[?refresh=1]` | none | `{providers: [ProviderInfo]}` |
| POST | `/v1/chat` | `{provider, model?, effort?, system?, messages}` | SSE |
| POST | `/v1/image` | `{provider, model?, effort?, prompt, size?}` | SSE, `done.images` |
| GET | `/v1/n8n` | none | n8n studio status, see the [README](../README.md#n8n-studio) |
| POST | `/v1/n8n/screenshot` | see the [README](../README.md#api) | SSE |

`ProviderInfo`:

```jsonc
{
  "id": "claude",                 // use as `provider`
  "name": "Claude Code",
  "available": true,              // installed and runnable; offer only these
  "version": "2.1.0",
  "path": "/opt/homebrew/bin/claude", // resolved CLI binary
  "default_model": "sonnet",      // used when `model` is omitted
  "models": [{ "id": "sonnet", "name": "Sonnet", "efforts": ["low", "high"] }],
  "efforts": ["low", "medium", "high"],
  "capabilities": ["chat"],       // "image" if it supports /v1/image
  "error": ""                     // why it is unavailable
}
```

Chat request:

```jsonc
{
  "provider": "claude",
  "model": "sonnet",              // optional
  "effort": "low",                // optional, one of the provider's efforts
  "system": "You are a concise assistant.",   // optional; replaces the default system prompt
  "messages": [                   // whole conversation; the bridge keeps no state
    { "role": "user", "content": "Hi" },
    { "role": "assistant", "content": "Hello!" },
    { "role": "user", "content": "Summarize: …" }
  ]
}
```

Image request (providers with the `image` capability, currently Codex):

```json
{ "provider": "codex", "prompt": "A watercolor fox, minimal background", "size": "1024x1024" }
```

`size` is `WIDTHxHEIGHT` or `auto`.

## Streaming format

Streaming endpoints answer `200 text/event-stream` with these events, in order:

| Event | Data |
|---|---|
| `start` | `{provider, model}` |
| `delta` | `{text}`, zero or more times; append them |
| `done` | `{text, provider, model, usage: {input_tokens, output_tokens}}` plus `images` for `/v1/image` |
| `error` | `{code, message}`, instead of `done` |

Use `fetch` and read the body stream. `EventSource` can't send POST bodies or headers. Codex and some models send the
whole answer in one `delta`.

## Errors

Before the stream starts, errors are JSON `{"error": {"code", "message"}}`:

| Status | Code | Meaning |
|---|---|---|
| 400 | `validation_error` | Bad JSON, empty messages, unknown role, bad model/effort/size |
| 400 | `unsupported` | The provider can't generate images |
| 401 | `unauthorized` | Missing or wrong token |
| 403 | `origin_not_allowed` | The website isn't on the allowlist |
| 403 | `host_not_allowed` | Not a loopback host |
| 404 | `unknown_provider` | No such provider id |
| 503 | `provider_unavailable` | CLI not installed or failing; `message` says why |
| 503 | `busy` | All slots in use (`max_concurrent`); retry shortly |

During the stream, the `error` event carries `provider_failed` (the CLI failed, e.g. not logged in or unknown model),
`timeout` or `no_image`. If `fetch` throws a `TypeError`, the bridge isn't running (or the browser blocked the
request).

## JavaScript client

A dependency-free client (TypeScript; remove the types for plain JS):

```ts
export class BridgeError extends Error {
  constructor(public code: string, message: string) {
    super(message)
  }
}

export interface BridgeConfig {
  url: string // e.g. http://127.0.0.1:7777
  token: string
}

async function request(cfg: BridgeConfig, path: string, init: RequestInit = {}) {
  let res: Response
  try {
    res = await fetch(cfg.url + path, {
      ...init,
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${cfg.token}`, ...init.headers },
    })
  } catch (err) {
    if ((err as Error).name === 'AbortError') throw err
    throw new BridgeError('bridge_offline', 'AI Bridge is not running on this computer')
  }
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    throw new BridgeError(body?.error?.code ?? `http_${res.status}`, body?.error?.message ?? res.statusText)
  }
  return res
}

export async function* stream(cfg: BridgeConfig, path: string, body: unknown, signal?: AbortSignal) {
  const res = await request(cfg, path, { method: 'POST', body: JSON.stringify(body), signal })
  const reader = res.body!.pipeThrough(new TextDecoderStream()).getReader()
  let buf = ''
  for (;;) {
    const { value, done } = await reader.read()
    if (done) return
    buf += value
    let i: number
    while ((i = buf.indexOf('\n\n')) >= 0) {
      const chunk = buf.slice(0, i)
      buf = buf.slice(i + 2)
      const event = chunk.match(/^event: (.*)$/m)?.[1]
      const data = chunk.match(/^data: (.*)$/m)?.[1]
      if (event && data) yield { event, data: JSON.parse(data) }
    }
  }
}

/** Streams a chat answer; onText gets the full text so far. Resolves with the `done` payload. */
export async function chat(cfg: BridgeConfig, body: object, onText?: (text: string) => void, signal?: AbortSignal) {
  let text = ''
  for await (const { event, data } of stream(cfg, '/v1/chat', body, signal)) {
    if (event === 'delta') onText?.((text += data.text))
    else if (event === 'done') return data as { text: string; model: string; usage: { input_tokens: number; output_tokens: number } }
    else if (event === 'error') throw new BridgeError(data.code, data.message)
  }
  throw new BridgeError('incomplete', 'The stream ended without an answer')
}

export async function providers(cfg: BridgeConfig) {
  const res = await request(cfg, '/v1/providers')
  return (await res.json()).providers.filter((p: any) => p.available)
}

/** "connected" | "wrong_token" | "origin_not_allowed" | "offline" */
export async function testConnection(cfg: BridgeConfig) {
  try {
    const res = await fetch(`${cfg.url}/health`, { headers: { Authorization: `Bearer ${cfg.token}` } })
    if (res.status === 403) return 'origin_not_allowed'
    return (await res.json()).authorized ? 'connected' : 'wrong_token'
  } catch {
    return 'offline'
  }
}
```

## Recipes

**Stream into an element, with a cancel button**

```ts
const cfg = { url: 'http://127.0.0.1:7777', token: localStorage.getItem('aiBridgeToken') ?? '' }
const ctrl = new AbortController()
cancelButton.onclick = () => ctrl.abort() // also kills the CLI run on the bridge

const result = await chat(cfg, {
  provider: 'claude',
  system: 'Summarize the user\'s note as 3 short bullet points.',
  messages: [{ role: 'user', content: note.text }],
}, (text) => (output.textContent = text), ctrl.signal)
console.log(result.model, result.usage)
```

**Structured output.** Ask for JSON in `system`, then parse and validate it:

```ts
const { text } = await chat(cfg, {
  provider: 'codex',
  system: 'Reply with JSON only: {"tags": string[], "sentiment": "positive" | "neutral" | "negative"}',
  messages: [{ role: 'user', content: review }],
})
const parsed = JSON.parse(text.replace(/^```(?:json)?\s*|\s*```$/g, ''))
```

**Image generation**

```ts
for await (const { event, data } of stream(cfg, '/v1/image', { provider: 'codex', prompt, size: '1024x1024' })) {
  if (event === 'done') img.src = `data:${data.images[0].mime};base64,${data.images[0].data}`
  if (event === 'error') throw new BridgeError(data.code, data.message)
}
```

**React hook**

```tsx
export function useBridgeChat(cfg: BridgeConfig) {
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const ctrl = useRef<AbortController>()

  const run = useCallback(async (body: object) => {
    ctrl.current?.abort()
    ctrl.current = new AbortController()
    setText(''); setError(null); setBusy(true)
    try {
      return await chat(cfg, body, setText, ctrl.current.signal)
    } catch (err) {
      if ((err as Error).name !== 'AbortError') setError((err as Error).message)
    } finally {
      setBusy(false)
    }
  }, [cfg.url, cfg.token])

  return { text, busy, error, run, cancel: () => ctrl.current?.abort() }
}
```

**From a script (no browser)**

```sh
curl -N http://127.0.0.1:7777/v1/chat \
  -H "Authorization: Bearer $(ai-bridge token)" -H 'Content-Type: application/json' \
  -d '{"provider":"gemini","messages":[{"role":"user","content":"Say hi"}]}'
```

## Providers

| id | CLI | Models | Efforts | Notes |
|---|---|---|---|---|
| `claude` | Claude Code (`claude -p`) | `sonnet`, `opus`, `fable`, `haiku` | `low` … `max` | Token-by-token streaming |
| `codex` | Codex (`codex exec`), ChatGPT login | from the Codex catalogue | per model | Answer arrives in one `delta`; can generate images |
| `gemini` | Gemini CLI (`gemini`), Google login | `auto`, `pro`, `flash`, `flash-lite` | none | Streams in chunks |

Read `/v1/providers` at runtime and don't hardcode this table: users have different CLIs and versions installed.

All CLIs run in an empty temporary directory with tools disabled: Claude with `--tools ""` and no user settings or
MCP, Codex in a read-only sandbox without user config, and Gemini with a deny-all tool policy, no extensions and no
MCP servers. Models can't read files, run commands or browse, so put everything they need in the request.

## Limits and behaviour

| | Default | Config key |
|---|---|---|
| Concurrent CLI runs | 2 | `max_concurrent` |
| Timeout per run | 300 s | `timeout_sec` |
| Request body | 2 MB | |
| Image prompt | 32 KB | |

Each request starts a new CLI process, so expect a few seconds of startup before the first `delta`. Conversations are
stateless: send the whole history every time (multi-turn messages are folded into one prompt).

## Troubleshooting

| Symptom | Fix |
|---|---|
| `fetch` throws `TypeError: Failed to fetch` | Start AI Bridge. If it is running, check the browser's local network permission for your site |
| `403 origin_not_allowed` | Allow the exact origin shown in the message (scheme, host and port) |
| `401 unauthorized` | Paste the current token. It changes when someone clicks **New token** |
| `503 provider_unavailable` | Install the CLI, then **Check again** in the app. If it is installed but not found, set its path under `binaries` in the config (see the [README](../README.md#finding-the-ai-clis)) |
| `provider_failed: … not logged in` / auth errors | Run the CLI once in a terminal and log in (`claude`, `codex login`, `gemini`) |
| `503 busy` | Wait, or raise `max_concurrent` |
| `timeout` | Shorten the input, use a faster model, or raise `timeout_sec` |

Use **Test** next to each AI tool in the app, or `ai-bridge test`, to check a CLI end to end. The **Activity** list shows
every request with its full request and response.
