SHELL := /bin/sh
VERSION ?= 0.1.0-alpha.6
EXE := $(if $(filter Windows_NT,$(OS)),.exe,)
export GOCACHE := $(CURDIR)/.cache/go
export CGO_ENABLED := 0
# Explicit patterns keep Go tooling out of web/node_modules, which can ship Go sources.
GO_PACKAGES := ./cmd/... ./internal/... ./scripts/...
GOFLAGS_BUILD := -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build binary frontend install test test-go test-web lint fmt demo demo-hub dev cross browser-test
install:
	npm ci --prefix web
frontend:
	npm --prefix web run build
build: frontend binary
binary:
	go build $(GOFLAGS_BUILD) -o bin/hclaro$(EXE) ./cmd/hclaro

test: test-go test-web
test-go:
	CGO_ENABLED=1 go test -race $(GO_PACKAGES)
test-web:
	npm --prefix web test
lint:
	go vet $(GO_PACKAGES)
	npm --prefix web run typecheck
	npm --prefix web run lint
fmt:
	gofmt -w cmd internal fixtures scripts/fake-engine scripts/fake-aws scripts/fixture-http scripts/hub-demo

bin/mock/terraform$(EXE): scripts/fake-engine/main.go fixtures/state.json
	go build -trimpath -buildvcs=false -o bin/mock/terraform$(EXE) ./scripts/fake-engine

bin/mock/tofu$(EXE): scripts/fake-engine/main.go fixtures/state.json
	go build -trimpath -buildvcs=false -o bin/mock/tofu$(EXE) ./scripts/fake-engine

# A synthetic AWS CLI for Cloud discovery; bin/mock is first on the demo PATH, so the real
# AWS CLI and real credentials are never used by the demo.
bin/mock/aws$(EXE): scripts/fake-aws/main.go $(wildcard fixtures/aws/*.json)
	go build -trimpath -buildvcs=false -o bin/mock/aws$(EXE) ./scripts/fake-aws

demo: binary bin/mock/terraform$(EXE) bin/mock/aws$(EXE)
	PATH="$(CURDIR)/bin/mock:$$PATH" TF_DATA_DIR="$(CURDIR)/fixtures/backend" HCLARO_CONFIG_DIR="$(CURDIR)/.cache/demo/config" ./bin/hclaro$(EXE) --engine terraform --path fixtures/project --no-browser

# Synthetic Hub projects and a registry under .cache/hub-demo; the real registry is untouched.
demo-hub: binary bin/mock/terraform$(EXE) bin/mock/tofu$(EXE)
	rm -rf .cache/hub-demo
	go run ./scripts/hub-demo .cache/hub-demo
	PATH="$(CURDIR)/bin/mock:$$PATH" HCLARO_CONFIG_DIR="$(CURDIR)/.cache/hub-demo/config" ./bin/hclaro$(EXE) --hub --no-browser

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
