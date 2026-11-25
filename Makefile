SHELL := /bin/sh
VERSION ?= 1.0.0
EXE := $(if $(filter Windows_NT,$(OS)),.exe,)
export GOCACHE := $(CURDIR)/.cache/go
export CGO_ENABLED := 0
# Explicit patterns keep Go tooling out of web/node_modules, which can ship Go sources.
GO_PACKAGES := ./cmd/... ./internal/... ./scripts/...
GOFLAGS_BUILD := -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build binary frontend install test test-go test-web lint fmt demo demo-hub dev cross browser-test brand-assets qualify
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
	gofmt -w cmd internal fixtures scripts/fake-engine scripts/fake-aws scripts/fake-aws-vault scripts/fake-gcloud scripts/fake-az scripts/fake-infracost scripts/fixture-http scripts/hub-demo scripts/qualify

# M27 real-engine harness (internal/qualify, docs/qualification/README.md). Maintainer-run, never
# in PR or push CI: make qualify TF=/abs/terraform TOFU=/abs/tofu MIRROR=<mirror dir> OUT=<dir>
qualify: binary
	TF="$(TF)" TOFU="$(TOFU)" MIRROR="$(MIRROR)" OUT="$(OUT)" scripts/qualify/qualify.sh

bin/mock/terraform$(EXE): scripts/fake-engine/main.go fixtures/state.json
	go build -trimpath -buildvcs=false -o bin/mock/terraform$(EXE) ./scripts/fake-engine

bin/mock/tofu$(EXE): scripts/fake-engine/main.go fixtures/state.json
	go build -trimpath -buildvcs=false -o bin/mock/tofu$(EXE) ./scripts/fake-engine

# A synthetic AWS CLI for Cloud discovery; bin/mock is first on the demo PATH, so the real
# AWS CLI and real credentials are never used by the demo.
bin/mock/aws$(EXE): scripts/fake-aws/main.go $(wildcard fixtures/aws/*.json)
	go build -trimpath -buildvcs=false -o bin/mock/aws$(EXE) ./scripts/fake-aws

# A synthetic gcloud for Google Cloud discovery (M33.B), same PATH rule as the AWS CLI above.
bin/mock/gcloud$(EXE): scripts/fake-gcloud/main.go internal/gcpcli/gcpclitest/gcpclitest.go $(wildcard fixtures/gcp/*.json)
	go build -trimpath -buildvcs=false -o bin/mock/gcloud$(EXE) ./scripts/fake-gcloud

# A synthetic az for Azure discovery (M33.C), same PATH rule.
bin/mock/az$(EXE): scripts/fake-az/main.go internal/azcli/azclitest/azclitest.go $(wildcard fixtures/azure/*.json)
	go build -trimpath -buildvcs=false -o bin/mock/az$(EXE) ./scripts/fake-az

# A synthetic Infracost CLI for cost estimates (M17); no pricing API is contacted.
bin/mock/infracost$(EXE): scripts/fake-infracost/main.go fixtures/cost/infracost-breakdown.json
	go build -trimpath -buildvcs=false -o bin/mock/infracost$(EXE) ./scripts/fake-infracost

demo: binary bin/mock/terraform$(EXE) bin/mock/aws$(EXE) bin/mock/gcloud$(EXE) bin/mock/az$(EXE) bin/mock/infracost$(EXE)
	@echo "Synthetic demo: fake Terraform/AWS/gcloud/az/Infracost CLIs and fixtures only. No real engine, AWS, Google Cloud or Azure account, provider or backend is used."
	PATH="$(CURDIR)/bin/mock:$$PATH" TF_DATA_DIR="$(CURDIR)/fixtures/backend" HCLARO_CONFIG_DIR="$(CURDIR)/.cache/demo/config" ./bin/hclaro$(EXE) --engine terraform --path fixtures/project --no-browser

# Synthetic Hub projects and a registry under .cache/hub-demo; the real registry is untouched.
demo-hub: binary bin/mock/terraform$(EXE) bin/mock/tofu$(EXE)
	@echo "Synthetic Hub demo: fake Terraform/OpenTofu CLIs and synthetic projects only. No real engine, provider or backend is used."
	rm -rf .cache/hub-demo
	go run ./scripts/hub-demo .cache/hub-demo
	PATH="$(CURDIR)/bin/mock:$$PATH" HCLARO_CONFIG_DIR="$(CURDIR)/.cache/hub-demo/config" ./bin/hclaro$(EXE) --hub --no-browser

dev: binary bin/mock/terraform$(EXE)
	node scripts/dev.mjs

# Regenerates the committed favicon, touch icon and docs/design/assets from the M19 reference geometry.
brand-assets:
	node scripts/brand-assets.mjs

browser-test: binary bin/mock/terraform$(EXE) bin/mock/tofu$(EXE) bin/mock/infracost$(EXE)
	go build -trimpath -o bin/fixture-http$(EXE) ./scripts/fixture-http
	node scripts/browser-smoke.mjs
	node scripts/browser-hub.mjs
	# Exit 77 means the check did not run on this platform: reported as SKIP, not as a pass.
	for t in quick-actions saved-conversations; do node tests/e2e/$$t.mjs; s=$$?; [ $$s -eq 77 ] && echo "SKIPPED $$t e2e" || [ $$s -eq 0 ] || exit $$s; done

cross:
	mkdir -p dist
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS_BUILD) -o dist/hclaro-darwin-amd64 ./cmd/hclaro
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS_BUILD) -o dist/hclaro-darwin-arm64 ./cmd/hclaro
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS_BUILD) -o dist/hclaro-linux-amd64 ./cmd/hclaro
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS_BUILD) -o dist/hclaro-linux-arm64 ./cmd/hclaro
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS_BUILD) -o dist/hclaro-windows-amd64.exe ./cmd/hclaro
