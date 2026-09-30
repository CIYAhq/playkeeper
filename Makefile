# Playkeeper developer commands. Run `make help`.
SHELL := /bin/bash
.DEFAULT_GOAL := help

# Prefer the pinned toolchains from scripts/setup.sh when present.
export PATH := $(CURDIR)/.tools/go/bin:$(CURDIR)/.tools/node/bin:$(PATH)
export CGO_ENABLED ?= 0

GO_PKGS := ./cmd/... ./internal/... ./web
SH_FILES := $(wildcard scripts/*.sh scripts/e2e/*.sh packaging/*.sh)

.PHONY: help setup check lint lint-go lint-web lint-notices lint-sh typecheck test test-go test-go-other test-agent test-web test-sh web-budget web build package notices site dev e2e-vm clean template-check template-thumbnails plugins

help: ## Show this help
	@awk 'BEGIN{FS=":.*## "} /^[a-z0-9-]+:.*## /{printf "  make %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

setup: ## Install pinned Go/Node into .tools/ and the web dependencies
	./scripts/setup.sh

check: lint typecheck test web-budget ## Everything CI's check job runs: lint, typecheck, unit tests, the first-load budget

lint: lint-go lint-web lint-notices ## gofmt, go vet, ESLint (web UI and browser tests), THIRD_PARTY_NOTICES up to date

lint-go:
	@unformatted=$$(gofmt -l cmd internal web/*.go); if [ -n "$$unformatted" ]; then echo "gofmt needed: $$unformatted"; exit 1; fi
	go vet $(GO_PKGS)

lint-web:
	cd web && npx eslint . ../test/e2e/ui

lint-notices:
	./scripts/third-party-notices.sh --check

lint-sh: ## shellcheck the shell scripts (needs shellcheck installed)
	shellcheck -x $(SH_FILES)

typecheck: ## TypeScript type check
	cd web && npx tsc --noEmit

test: test-go test-web test-sh ## Go, web and installer-script unit tests

# The agent's tests take longer than go test's default ten minutes on CI runners.
test-go:
	go test -count=1 -timeout 30m $$(scripts/quarantine.sh skip) $(GO_PKGS)

test-go-other: ## Go unit tests of every package but the agent's
	go test -count=1 -timeout 30m $$(scripts/quarantine.sh skip) $$(go list $(GO_PKGS) | grep -v '/internal/agent$$')

# The agent's tests mostly wait on timers, so they run in shards side by
# side, balanced by the times in scripts/agent-test-times.txt.
test-agent: ## The agent's unit tests in 16 shards side by side (JOBS=n for another number, SHARD=k/n for one shard)
	$(if $(SHARD),./scripts/go-test-shard.sh ./internal/agent $(SHARD) $(OUT),./scripts/go-test-shard.sh --jobs $(or $(JOBS),16) ./internal/agent $(or $(OUT),$$(mktemp -d)) scripts/agent-test-times.txt)

test-web:
	cd web && npx vitest run

test-sh:
	bash packaging/get_test.sh
	bash scripts/setup_test.sh
	bash scripts/package_test.sh
	bash scripts/quarantine_test.sh
	bash scripts/demo-marker_test.sh
	bash scripts/go-test-shard_test.sh
	bash scripts/ci-parts_test.sh
	bash scripts/ci-since_test.sh
	bash scripts/discord-feed_test.sh
	bash scripts/net-retry_test.sh
	bash scripts/e2e/vm-rehearsal_test.sh

# A production build into a folder of its own, so web/dist stays as it was:
# its first-load plugin (web/src/lib/first-load.ts) fails it over the budget.
web-budget: ## Fail when a page loads more JavaScript than web/src/lib/first-load.ts allows
	out=$$(mktemp -d) && trap 'rm -rf "$$out"' EXIT && cd web && npx vite build --outDir "$$out"

web: ## Build the browser UI into web/dist
	cd web && npm run build

build: web ## Build ./dist/playkeeper for this machine
	go build -trimpath -o dist/playkeeper ./cmd/playkeeper

package: ## Build dist/playkeeper-<version>-linux-{amd64,arm64}.tar.gz and the one-line installer assets
	./scripts/package.sh

plugins: ## Build the plugins that ship inside Playkeeper into internal/addons/firstparty (needs Java 21)
	cd plugins/ai-build-battle && ./gradlew --no-daemon -q clean build
	cp plugins/ai-build-battle/build/libs/ai-build-battle-*.jar internal/addons/firstparty/ai-build-battle.jar

notices: ## Regenerate THIRD_PARTY_NOTICES after changing Go or npm dependencies
	./scripts/third-party-notices.sh

site: ## Build playkeeper.io into site/dist (html/ is the web root; site/README.md)
	go run ./cmd/site

template-check: ## Create and start a server from each site template on a running Playkeeper (SOCKET=.dev/agent.sock, ARGS="-only towny -write -pin")
	go run ./cmd/template-check -socket $(or $(SOCKET),.dev/agent.sock) $(ARGS)

# A template that fails its check, its capture or its render still leaves the
# others' pictures, so they're drawn and written before the failure is
# reported. shots.py writes the 16:10 captures at the site's widths and needs
# Pillow 11.3 or newer (PYTHON=).
template-thumbnails: ## Thumbnails of the site's templates from their real worlds, on playkeeper dev run with PLAYKEEPER_E2E_OFFLINE_MODE_UNSAFE=1 (SOCKET=, ARGS="-only towny")
	cd site/tools/thumbnails && npm ci --no-audit --no-fund && npm run build
	rm -rf $(or $(SHOTS),.tmp/shots)
	status=0; go run ./cmd/template-check -socket $(or $(SOCKET),.dev/agent.sock) -shots $(or $(SHOTS),.tmp/shots) $(ARGS) || status=$$?; \
	if ls $(or $(SHOTS),.tmp/shots)/*.json.gz >/dev/null 2>&1; then \
	  node site/tools/thumbnails/render.mjs $(or $(SHOTS),.tmp/shots) || status=1; \
	fi; \
	if ls $(or $(SHOTS),.tmp/shots)/captures/templates/*.png >/dev/null 2>&1; then \
	  $(or $(PYTHON),python3) site/tools/shots.py $(or $(SHOTS),.tmp/shots)/captures || status=1; \
	fi; \
	exit $$status

dev: web ## Run agent + panel locally (state in .dev/, uses your Docker)
	go run ./cmd/playkeeper dev --dir .dev

e2e-vm: ## Full install/backup/second-host restore rehearsal in two KVM guests
	./scripts/e2e/vm-e2e.sh

clean: ## Remove build output (keeps .tools and .dev)
	rm -rf dist web/dist site/dist
