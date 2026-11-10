# AI Interception and Telemetry Proxy — local orchestration.
#
# Primary entry point is `make run` (acceptance criterion #1). It generates the
# dev CAs if missing, builds images, starts the stack, and prints connection
# info including a HOST-REACHABLE IP and TCP port 443.

SHELL := /usr/bin/env bash
COMPOSE := docker compose
CERTS_DIR := $(CURDIR)/certs
export CERTS_DIR

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: ca
ca: ## Generate the dev interception CA (root + intermediate) and fixture certs if missing
	@if [[ ! -f "$(CERTS_DIR)/root-ca.crt" ]]; then \
		bash scripts/generate-dev-ca.sh; \
	else echo "interception CA present (certs/root-ca.crt); skipping"; fi
	@bash scripts/generate-fixture-certs.sh

.PHONY: build
build: ## Build all container images
	$(COMPOSE) build

.PHONY: up
up: ## Everything, locally, one command: proxy (real upstream), observer and processor in Docker; the API and UI here (docs/SETUP.md)
	@bash scripts/up.sh

.PHONY: verify
verify: ## Prove the local stack end to end: one request through the proxy, seen by the observer, processor and API
	@bash scripts/verify.sh

.PHONY: remote
remote: ## The UI here against the cluster's API (a kubectl port-forward tunnel to svc/api on :8787); needs api.enabled in the chart
	@bash scripts/remote.sh

.PHONY: down
down: ## Stop what `make up` or `make remote` started (data in data/ stays)
	@bash scripts/down.sh

.PHONY: run
run: ca build ## Generate CAs, build, start the full local stack, and print connection info
	$(COMPOSE) up -d
	@echo "Waiting for services to become healthy..."
	@sleep 3
	@bash scripts/print-connection-info.sh

.PHONY: info
info: ## Print connection info (DNS + trust config) for a test client
	@bash scripts/print-connection-info.sh

.PHONY: logs
logs: ## Tail logs for all services (observer prints JSON events)
	$(COMPOSE) logs -f

.PHONY: observer-logs
observer-logs: ## Tail only the Go observer's event output
	$(COMPOSE) logs -f observer

.PHONY: stop
stop: ## Stop the stack (containers down; CA material preserved)
	$(COMPOSE) down

.PHONY: test
test: ## Run the Go unit tests and the end-to-end smoke test
	cd aintercept && go test ./...
	bash scripts/smoke-test.sh

.PHONY: test-aws
test-aws: ca ## Test AWS support (KMS signing, Secrets Manager salt) against local emulators
	docker compose -f docker-compose.yml -f docker-compose.aws.yml up -d local-kms moto
	bash scripts/dev-aws-emulators.sh
	cd aintercept && CERTSVC_KMS_TEST_ENDPOINT=http://localhost:5056 go test -count=1 ./internal/ca/... ./internal/certsvc/... -run 'KMS|Emulator' -v
	cd aintercept && OBSERVER_SM_TEST_ENDPOINT=http://localhost:5055 go test -count=1 ./internal/observer -run Salt -v
	COMPOSE_FILE=docker-compose.yml:docker-compose.aws.yml docker compose up -d --build
	@echo "Waiting for the proxy to serve a KMS-signed leaf..."
	@for i in $$(seq 60); do \
		curl -s --cacert $(CERTS_DIR)/root-ca.crt --resolve api.anthropic.com:443:127.0.0.1 https://api.anthropic.com/ -o /dev/null && break; \
		sleep 1; \
	done
	COMPOSE_FILE=docker-compose.yml:docker-compose.aws.yml bash scripts/smoke-test.sh

.PHONY: unit
unit: ## Run only Go unit tests
	cd aintercept && go test ./...

.PHONY: schema-report
schema-report: ## Build+run the unrecognized-shape report over the object store (reads OBSERVER_S3_* env). Pass ARGS="--since <ns> --redact-keys".
	cd aintercept && go run ./cmd/schemareport $(ARGS)

.PHONY: client-event-registry
client-event-registry: ## Merge client_event shapes from the object store into the observer's per-client registries (reads OBSERVER_S3_* env; aws CLI). Review the diff, then rebuild.
	@mkdir -p .cache/telemetry
	AWS_ACCESS_KEY_ID="$$OBSERVER_S3_ACCESS_KEY_ID" AWS_SECRET_ACCESS_KEY="$$OBSERVER_S3_SECRET_ACCESS_KEY" \
		aws s3 sync "s3://$$OBSERVER_S3_BUCKET/$$OBSERVER_S3_PREFIX/" .cache/telemetry/ --only-show-errors \
		--exclude 'schema-samples/*' --region "$$OBSERVER_S3_REGION" $${OBSERVER_S3_ENDPOINT:+--endpoint-url "$$OBSERVER_S3_ENDPOINT"}
	python3 -I scripts/client_event_registry.py aintercept/internal/events/client_events .cache/telemetry
	@git --no-pager diff --stat aintercept/internal/events/client_events

# --- UI and query API (docs/ui/BUILD_PLAN.md). The UI needs Node 24 (ui/.nvmrc).
AINTERCEPT_DATA_DIR ?= $(CURDIR)/data/telemetry
export AINTERCEPT_DATA_DIR

AINTERCEPT_SHAPED_DIR ?= $(CURDIR)/data/shaped
export AINTERCEPT_SHAPED_DIR

.PHONY: process
process: ## Run the processor: $AINTERCEPT_DATA_DIR's raw partitions -> Parquet tables in $AINTERCEPT_SHAPED_DIR (compose runs it too)
	cd aintercept && PROCESS_LOCAL_DIR="$(AINTERCEPT_DATA_DIR)" PROCESS_OUT_DIR="$(AINTERCEPT_SHAPED_DIR)" PROCESS_POLL_SECONDS=2 go run ./cmd/aintercept process

.PHONY: api
api: ## Run the query API on :8787 over the processor's tables in $AINTERCEPT_SHAPED_DIR (JSON logs on stdout)
	cd api && uv run python -m aintercept_api.serve --port 8787 --reload

.PHONY: ui
ui: ## Run the UI dev server on :5173 (proxies /api to :8787)
	cd ui && pnpm dev

.PHONY: storybook
storybook: ## Run Storybook on :6006
	cd ui && pnpm storybook

.PHONY: regen
regen: ## Regenerate the UI contract: OpenAPI -> ui/src/api/spec.ts, then API test samples -> ui/src/api/samples
	cd api && uv run python -m aintercept_api.openapi > ../ui/openapi.json
	cd ui && pnpm gen:spec
	rm -rf ui/src/api/samples
	cd api && uv run pytest -q --write-samples

GENERATED := ui/openapi.json ui/src/api/spec.ts ui/src/api/samples

.PHONY: check-generated
check-generated: regen ## Fail if generated contract files differ from HEAD or don't typecheck (run `make regen` and commit)
	@git diff --exit-code --stat HEAD -- $(GENERATED) \
		&& test -z "$$(git ls-files --others --exclude-standard -- $(GENERATED))" \
		&& git ls-files --error-unmatch ui/openapi.json ui/src/api/spec.ts >/dev/null \
		|| { echo "Generated files are stale: run 'make regen' and commit."; exit 1; }
	cd ui && pnpm typecheck

# Everything CI requires (.github/workflows/ui.yml). Green here = green there.
.PHONY: check
check: check-go check-api check-ui check-repo check-generated ## All required checks: run before every commit

GO_MODULES := aintercept fixture

.PHONY: check-go
check-go: ## Go: gofmt clean, go vet, go test (with -race) in every module
	@unformatted="$$(gofmt -l $(GO_MODULES))"; test -z "$$unformatted" || { echo "gofmt needed:"; echo "$$unformatted"; exit 1; }
	for m in $(GO_MODULES); do (cd $$m && go vet ./...) || exit 1; done
	cd aintercept && go test -race -count=1 ./...

.PHONY: check-api
check-api: ## API: ruff (incl. security rules), format, mypy strict, tests with a 90% coverage floor, requirements.json
	cd api && uv run ruff check . && uv run ruff format --check . && uv run mypy && uv run pytest -q --cov

.PHONY: check-ui
check-ui: ## UI: ESLint (story per component, formatting, a11y, ...), lint-ui (tokens, channels, copy), typecheck, tests, requirements.json
	cd ui && pnpm lint && pnpm typecheck && pnpm test

.PHONY: check-repo
check-repo: ## Repo: exact pins and release-age rule, no real identifiers, no secrets in history
	python3 scripts/check-pins.py
	python3 scripts/check-real-data.py
	@command -v gitleaks >/dev/null || { echo "gitleaks not installed (brew install gitleaks)"; exit 1; }
	gitleaks git . --no-banner --redact

.PHONY: a11y
a11y: ## Accessibility: every story in both themes through axe-core (WCAG 2 A/AA); report in ui/parity/a11y-report.md. A11Y="Button Person" limits it
	@cd ui && pnpm -s build-storybook --quiet -o ../.cache/parity-storybook >/dev/null
	@cd ui && node parity/a11y.mjs ../.cache/parity-storybook $(A11Y)

.PHONY: latency
latency: ## Latency review: every page with the API normal, slow (9 s) and failing (500); needs the app running (make up)
	@cd ui && node parity/latency.mjs $(UI_URL)

.PHONY: hover
hover: ## Hover latency: sweeps the pointer over the person and session timelines; fails over 50 ms p95 (needs make up or make remote)
	@cd ui && node parity/hover-latency.mjs $(UI_URL)

.PHONY: parity
parity: ## Design parity: measure our Canvas stories against the Claude Design canvas (boxes + computed styles); report in ui/parity/report.md. PARITY="Button Chip" limits it
	@cd ui && pnpm -s build-storybook --quiet -o ../.cache/parity-storybook >/dev/null
	@cd ui && node parity/parity.mjs ../.cache/parity-storybook $(PARITY)

.PHONY: test-ui
test-ui: check-api check-ui ## Alias: API and UI checks

.PHONY: clean
clean: ## Stop the stack AND delete generated CA keys/certs (DESTRUCTIVE)
	@echo "WARNING: this deletes the dev CA private keys and all generated certs in $(CERTS_DIR)."
	@echo "A test client trusting the old root will need re-provisioning after regeneration."
	@read -p "Type 'yes' to proceed: " ans; \
	if [[ "$$ans" == "yes" ]]; then \
		$(COMPOSE) down -v || true; \
		find "$(CERTS_DIR)" -mindepth 1 -not -name '.gitignore' -delete; \
		echo "CA material and certs removed."; \
	else echo "Aborted."; fi
