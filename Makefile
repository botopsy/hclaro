SHELL := /bin/sh
VERSION ?= 0.1.0-alpha.1
EXE := $(if $(filter Windows_NT,$(OS)),.exe,)
export GOCACHE := $(CURDIR)/.cache/go
export CGO_ENABLED := 0
GOFLAGS_BUILD := -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build binary frontend install test test-go test-web lint fmt demo dev cross browser-test
install:
	npm install --prefix web
frontend:
	npm --prefix web run build
build: frontend binary
binary:
	go build $(GOFLAGS_BUILD) -o bin/hclaro$(EXE) ./cmd/hclaro

test: test-go test-web
test-go:
	CGO_ENABLED=1 go test -race ./...
test-web:
	npm --prefix web test
lint:
	go vet ./...
	npm --prefix web run typecheck
	npm --prefix web run lint
fmt:
	gofmt -w cmd internal fixtures scripts/fake-engine scripts/fixture-http

bin/mock/terraform$(EXE): scripts/fake-engine/main.go fixtures/state.json
	go build -trimpath -buildvcs=false -o bin/mock/terraform$(EXE) ./scripts/fake-engine

demo: binary bin/mock/terraform$(EXE)
	PATH="$(CURDIR)/bin/mock:$$PATH" TF_DATA_DIR="$(CURDIR)/fixtures/backend" ./bin/hclaro$(EXE) --engine terraform --path fixtures/project --no-browser

dev: binary bin/mock/terraform$(EXE)
	node scripts/dev.mjs

browser-test:
	go build -trimpath -o bin/fixture-http$(EXE) ./scripts/fixture-http
	node scripts/browser-smoke.mjs

cross:
	mkdir -p dist
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS_BUILD) -o dist/hclaro-darwin-amd64 ./cmd/hclaro
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS_BUILD) -o dist/hclaro-darwin-arm64 ./cmd/hclaro
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS_BUILD) -o dist/hclaro-linux-amd64 ./cmd/hclaro
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS_BUILD) -o dist/hclaro-linux-arm64 ./cmd/hclaro
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS_BUILD) -o dist/hclaro-windows-amd64.exe ./cmd/hclaro
