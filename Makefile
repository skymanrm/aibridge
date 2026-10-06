VERSION ?= 0.5.0
WAILS ?= $(shell command -v wails || echo $(HOME)/go/bin/wails)
LDFLAGS = -X github.com/skymanrm/aibridge/bridge.Version=$(VERSION)

.PHONY: app app-universal app-windows cli test

# build/bin/AI Bridge.app
app:
	$(WAILS) build -platform darwin/arm64 -ldflags "$(LDFLAGS)"

# build/bin/AI Bridge.app for Apple Silicon and Intel
app-universal:
	$(WAILS) build -platform darwin/universal -ldflags "$(LDFLAGS)"

# build/bin/AI Bridge.exe (cross-compiles from macOS; add -nsis on Windows for the installer)
app-windows:
	$(WAILS) build -platform windows/amd64 -ldflags "$(LDFLAGS)"

# ./ai-bridge headless binary
cli:
	go build -ldflags "-s -w $(LDFLAGS)" -o ai-bridge ./cmd/ai-bridge

test:
	go test -race ./bridge/...
