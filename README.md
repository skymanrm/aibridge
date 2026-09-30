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

Open **AI Bridge.app**: it starts the bridge on `127.0.0.1:7777` and stays in the Dock while it runs (a badge shows
requests in flight). Closing the window stops the bridge. The window shows a live map of websites → bridge → AI tools,
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
| GET | `/v1/providers[?refresh=1]` | token | Detected CLIs: `{providers: [{id, name, available, version, default_model, models: [{id, name, efforts?}], efforts, error}]}` |
| POST | `/v1/chat` | token | `{provider, model?, effort?, system, messages: [{role: user\|assistant, content}]}` → SSE `start`, `delta {text}`*, `done {text, provider, model, usage}` or `error {code, message}` |

Auth is `Authorization: Bearer <token>`. Codex emits its answer as a single `delta` (its CLI does not stream tokens).

## Security

- Listens on `127.0.0.1` only; requests with a non-loopback `Host` are rejected (DNS rebinding).
- Browser requests must come from an allowlisted `Origin`; every call except `/health` needs the token.
- CLIs run in an empty temp dir with no tools (`claude --tools ""`, no user settings/MCP) or a read-only sandbox
  (`codex --sandbox read-only --ignore-user-config`), so a prompt cannot touch your files.
