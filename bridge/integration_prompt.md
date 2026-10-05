You are working in my web app's codebase. Integrate it with **AI Bridge**, a small app running on my computer that exposes my locally installed AI CLIs (Claude Code, Codex, Gemini CLI) over HTTP on the loopback interface. Requests run under my own CLI logins, so the web app needs no API keys and no backend for AI features: the browser calls the bridge directly.

## What to build

{{if .Task}}{{.Task}}{{else}}Look at the app and propose 2-3 places where an AI feature would genuinely help users, then ask me which one to build before writing code.{{end}}

## How AI Bridge works

- Base URL: `{{.BaseURL}}` (loopback only; only `127.0.0.1`, `localhost` and `[::1]` hosts are accepted).
- Every endpoint except `GET /health` requires `Authorization: Bearer <token>`.
- Browser requests must come from an allowed website origin. Allowed right now:
{{- if .Origins}}{{range .Origins}}
  - `{{.}}`{{end}}{{else}} none yet.{{end}}
  If the app's origin (including the dev server, e.g. `http://localhost:5173`) is missing, tell me to add it in the AI Bridge window under **Websites** or with `ai-bridge allow <origin>`. Disallowed origins get `403 origin_not_allowed`.
- The token is personal to each user's machine. **Never hardcode or commit it.** Add a settings field where the user pastes it (they copy it from the AI Bridge window or with `ai-bridge token`), keep it in `localStorage`, and let the user change the bridge URL too (default `{{.BaseURL}}`).
- Requests run one CLI process each; at most {{.MaxConcurrent}} run at once (more get `503 busy`), each up to {{.TimeoutSec}} s. Request bodies are limited to 2 MB.

### Endpoints

| Method | Path | Body / result |
|---|---|---|
| GET | `/health` | `{name: "ai-bridge", version, authorized}`, no token needed; `authorized` tells whether the sent token is valid |
| GET | `/v1/providers` | `{providers: [{id, name, available, version, default_model, models: [{id, name, efforts?}], efforts, capabilities, error}]}` |
| POST | `/v1/chat` | `{provider, model?, effort?, system?, messages: [{role: "user" \| "assistant", content}]}` → SSE stream |
| POST | `/v1/image` | providers with the `image` capability: `{provider, model?, effort?, prompt, size?: "1024x1024" \| "auto"}` → SSE stream, `done.images = [{mime, data (base64)}]` |

Streaming responses are server-sent events over a `fetch` POST (not `EventSource`), in this order: `start {provider, model}`, then `delta {text}` zero or more times, then exactly one of `done {text, provider, model, usage: {input_tokens, output_tokens}}` or `error {code, message}`. Codex and some models send the whole answer in one `delta`, so do not assume token-by-token streaming.

Errors before streaming starts are JSON `{error: {code, message}}` with an HTTP status: `400 validation_error`, `401 unauthorized`, `403 origin_not_allowed`, `404 unknown_provider`, `503 provider_unavailable` or `503 busy`. Errors during streaming arrive as the SSE `error` event (`provider_failed`, `timeout`, `no_image`). If `fetch` itself throws a `TypeError`, the bridge is not running: show a friendly "Start AI Bridge" hint, not a crash.

There is no tool use, file access or web browsing: the CLIs run sandboxed with tools disabled. Put everything the model needs (documents, data, instructions) into `system` and `messages`. `system` replaces the default system prompt. Multi-turn chat is stateless: send the whole conversation each time.

### Providers on this machine right now
{{if .Providers}}{{range .Providers}}
- `{{.ID}}` ({{.Name}}{{if .Version}} {{.Version}}{{end}}): models {{range $i, $m := .Models}}{{if $i}}, {{end}}`{{$m.ID}}`{{end}}; default `{{.DefaultModel}}`
{{- if .Efforts}}; efforts {{range $i, $e := .Efforts}}{{if $i}}, {{end}}`{{$e}}`{{end}}{{end}}
{{- if .Image}}; can generate images{{end}}{{end}}{{else}}
None detected. Load the list from `GET /v1/providers` at runtime anyway.{{end}}

Other users will have different CLIs installed, so read `/v1/providers` at runtime, only offer providers with `available: true`, and let the user pick the provider and model (remember the choice in `localStorage`).

## Reference client

Adapt this to the app's language and conventions:

```ts
export type BridgeEvent =
  | { event: 'start'; data: { provider: string; model: string } }
  | { event: 'delta'; data: { text: string } }
  | { event: 'done'; data: { text: string; model: string; usage: { input_tokens: number; output_tokens: number } } }
  | { event: 'error'; data: { code: string; message: string } }

export class BridgeError extends Error {
  constructor(public code: string, message: string) { super(message) }
}

export async function* bridgeStream(baseUrl: string, token: string, path: string, body: unknown, signal?: AbortSignal) {
  let res: Response
  try {
    res = await fetch(`${baseUrl}${path}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
      body: JSON.stringify(body),
      signal,
    })
  } catch (err) {
    if ((err as Error).name === 'AbortError') throw err
    throw new BridgeError('bridge_offline', 'AI Bridge is not running on this computer')
  }
  if (!res.ok) {
    const e = await res.json().catch(() => null)
    throw new BridgeError(e?.error?.code ?? 'http_' + res.status, e?.error?.message ?? res.statusText)
  }
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
      if (event && data) yield { event, data: JSON.parse(data) } as BridgeEvent
    }
  }
}

// Usage: stream an answer into the UI.
export async function ask(baseUrl: string, token: string, prompt: string, onText: (text: string) => void, signal?: AbortSignal) {
  let text = ''
  for await (const ev of bridgeStream(baseUrl, token, '/v1/chat', {
    provider: 'claude',
    system: 'You are a concise assistant.',
    messages: [{ role: 'user', content: prompt }],
  }, signal)) {
    if (ev.event === 'delta') onText((text += ev.data.text))
    if (ev.event === 'done') return ev.data
    if (ev.event === 'error') throw new BridgeError(ev.data.code, ev.data.message)
  }
  throw new BridgeError('incomplete', 'The stream ended without an answer')
}
```

## Requirements

- Put the bridge client in its own module; keep UI code free of fetch/SSE details.
- Settings UI: bridge URL, token (password field with show/hide), a **Test connection** button that calls `/health` with the token and reports "connected", "wrong token" (`authorized: false`), "website not allowed" (403) or "bridge not running" (fetch error), plus provider/model pickers filled from `/v1/providers`.
- AI features render the streamed text as it arrives, show progress, can be cancelled (`AbortController`; aborting the fetch kills the CLI run) and show the bridge's error messages plainly.
- When the bridge is not configured, hide or disable AI features with a short hint instead of failing.
- Keep prompts in one place, ask for structured output (e.g. JSON) when the app parses the answer, and validate it.
- Chromium may ask the user to allow the site to access apps on their device (local network access) the first time it calls the bridge; mention this in the settings hint.
