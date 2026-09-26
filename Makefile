# Playkeeper developer commands. Run `make help`.
SHELL := /bin/bash
.DEFAULT_GOAL := help

# Prefer the pinned toolchains from scripts/setup.sh when present.
export PATH := $(CURDIR)/.tools/go/bin:$(CURDIR)/.tools/node/bin:$(PATH)
export CGO_ENABLED ?= 0

GO_PKGS := ./cmd/... ./internal/... ./web
SH_FILES := $(wildcard scripts/*.sh scripts/e2e/*.sh packaging/*.sh)

.PHONY: help setup check lint lint-go lint-web lint-notices lint-sh typecheck test test-go test-web test-sh web build package notices dev e2e-vm clean

help: ## Show this help
	@awk 'BEGIN{FS=":.*## "} /^[a-z0-9-]+:.*## /{printf "  make %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

setup: ## Install pinned Go/Node into .tools/ and the web dependencies
	./scripts/setup.sh

check: lint typecheck test ## Everything CI's check job runs: lint, typecheck, unit tests

lint: lint-go lint-web lint-notices ## gofmt, go vet, ESLint, THIRD_PARTY_NOTICES up to date

lint-go:
	@unformatted=$$(gofmt -l cmd internal web/*.go); if [ -n "$$unformatted" ]; then echo "gofmt needed: $$unformatted"; exit 1; fi
	go vet $(GO_PKGS)

lint-web:
	cd web && npx eslint .

lint-notices:
	./scripts/third-party-notices.sh --check

lint-sh: ## shellcheck the shell scripts (needs shellcheck installed)
	shellcheck -x $(SH_FILES)

typecheck: ## TypeScript type check
	cd web && npx tsc --noEmit

test: test-go test-web test-sh ## Go, web and installer-script unit tests

# The agent's tests take longer than go test's default ten minutes on CI runners.
test-go:
	go test -count=1 -timeout 20m $(GO_PKGS)

test-web:
	cd web && npx vitest run

test-sh:
	bash packaging/get_test.sh
	bash scripts/setup_test.sh
	bash scripts/package_test.sh

web: ## Build the browser UI into web/dist
	cd web && npm run build

build: web ## Build ./dist/playkeeper for this machine
	go build -trimpath -o dist/playkeeper ./cmd/playkeeper

package: ## Build dist/playkeeper-<version>-linux-amd64.tar.gz and the one-line installer assets
	./scripts/package.sh

notices: ## Regenerate THIRD_PARTY_NOTICES after changing Go or npm dependencies
	./scripts/third-party-notices.sh

dev: web ## Run agent + panel locally (state in .dev/, uses your Docker)
	go run ./cmd/playkeeper dev --dir .dev

e2e-vm: ## Full install/backup/second-host restore rehearsal in two KVM guests
	./scripts/e2e/vm-e2e.sh

clean: ## Remove build output (keeps .tools and .dev)
	rm -rf dist web/dist
