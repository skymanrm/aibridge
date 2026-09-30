VERSION ?= 0.2.0
WAILS ?= $(shell command -v wails || echo $(HOME)/go/bin/wails)
LDFLAGS = -X git.home.fanyagin.ru/personal/ai-bridge/bridge.Version=$(VERSION)

.PHONY: app cli test

# build/bin/AI Bridge.app
app:
	$(WAILS) build -platform darwin/arm64 -ldflags "$(LDFLAGS)"

# ./ai-bridge headless binary
cli:
	go build -ldflags "-s -w $(LDFLAGS)" -o ai-bridge ./cmd/ai-bridge

test:
	go test -race ./bridge/...
