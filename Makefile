DATE := $(shell date -u +%Y-%m-%d)
VERSION := $(shell grep -m1 'const Version' internal/config/config.go | sed 's/.*"//;s/".*//')
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

LDFLAGS := -s -w \
	-X autopr/cmd/autopr/cli.version=$(VERSION) \
	-X autopr/cmd/autopr/cli.commit=$(COMMIT) \
	-X autopr/cmd/autopr/cli.date=$(DATE)

.PHONY: build
build:
	go build -ldflags "$(LDFLAGS)" -o ap ./cmd/autopr
