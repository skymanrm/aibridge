# ai-bridge

Lets your web apps use the AI CLIs installed on this Mac — **Claude Code** (`claude`) and **Codex** (`codex`, ChatGPT login) —
through a small HTTP API on `127.0.0.1`. Runs use your own CLI logins; no API keys leave the machine.

```
https://your-app (browser) ──fetch/SSE──> 127.0.0.1:7777 (ai-bridge) ──> claude -p … / codex exec …
```

## App

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0   # once
make app                                                     # -> build/bin/AI Bridge.app
```

Open **AI Bridge.app**: it starts the bridge on `127.0.0.1:7777` and lives in the menu bar, not the Dock (the icon dims
when the bridge is stopped and shows the number of requests in flight). Closing the window keeps the bridge running;
reopen it or quit from the menu bar icon. The window shows a live map of websites → bridge → AI tools,
the detected CLIs, allowed websites, the token and recent requests.

## Headless CLI

```sh
make cli
./ai-bridge allow https://telegafarm.home.fanyagin.ru   # allow a website (applies without restart)
./ai-bridge token                                       # paste into the web app's AI settings
./ai-bridge serve                                       # same server, no window
```

Both share `~/.config/ai-bridge/config.json` (`port`, `token`, `origins`, `max_concurrent`, `timeout_sec`,
optional `binaries: {"claude": "/path/to/claude"}`).

## API

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/health` | — | `{name, version, authorized}` |
| GET | `/v1/providers[?refresh=1]` | token | Detected CLIs: `{providers: [{id, name, available, version, default_model, models: [{id, name, efforts?}], efforts, capabilities, error}]}` |
| POST | `/v1/chat` | token | `{provider, model?, effort?, system, messages: [{role: user\|assistant, content}]}` → SSE `start`, `delta {text}`*, `done {text, provider, model, usage}` or `error {code, message}` |
| POST | `/v1/image` | token | Providers with the `image` capability (Codex): `{provider, model?, effort?, prompt, size?: "WxH"\|"auto"}` → SSE `start`, `delta {text}`*, `done {images: [{mime, data (base64)}], text, provider, model, usage}` or `error` |

| GET | `/v1/n8n` | token | n8n studio status: `{available (docker found), running, n8n_version, editor_url, error}` |
| POST | `/v1/n8n/screenshot` | token | `{provider, model?, effort?, prompt \| workflow, size?: "WxH" (CSS px, rendered at 2x), theme?: light\|dark, frame?: editor\|canvas, layout?: auto\|keep}` → SSE `start`, `delta {text}`* (progress), `done {images, workflow, issues, notes, attempts, n8n_version, text, …}` or `error` |

Auth is `Authorization: Bearer <token>`. Codex emits its answer as a single `delta` (its CLI does not stream tokens).
`/v1/image` uses Codex's built-in `image_gen` tool; the files it saves under `$CODEX_HOME/generated_images/<thread>/` are
returned inline and then deleted.

## n8n studio

`/v1/n8n/screenshot` makes real n8n editor screenshots for posts about automation. Everything runs in Docker
(OrbStack / Docker Desktop); only the `docker` CLI is needed on the host:

- `bridge/studio/` (embedded, written to `~/.config/ai-bridge/studio/`) is a Compose project `ai-bridge-studio`:
  `n8nio/n8n` (pinned) and a Playwright capture service on `127.0.0.1:7778`. The first request runs
  `docker compose up --build --wait` (a few minutes once); later requests reuse the running stack.
- The AI designs the workflow JSON (`bridge/n8n_prompt.md`); the capture service checks node types against n8n's catalogue,
  fixes versions, attaches placeholder credentials, imports it, reads node warnings from the canvas and screenshots it.
  Unknown nodes and warnings go back to the AI (up to 3 attempts); the imported workflow is deleted afterwards.
- Pass `workflow` instead of `prompt` to render your own workflow JSON without AI.
- The studio's n8n is open at http://127.0.0.1:7779 (`studio@ai-bridge.local` / `AiBridge-Studio-1`).
  Stop it with `docker compose -p ai-bridge-studio down`.

## Security

- Listens on `127.0.0.1` only; requests with a non-loopback `Host` are rejected (DNS rebinding).
- Browser requests must come from an allowlisted `Origin`; every call except `/health` needs the token.
- CLIs run in an empty temp dir with no tools (`claude --tools ""`, no user settings/MCP) or a read-only sandbox
  (`codex --sandbox read-only --ignore-user-config`), so a prompt cannot touch your files.
