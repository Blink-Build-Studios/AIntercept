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
	cd observer && go test ./...
	cd certsvc && go test ./...
	bash scripts/smoke-test.sh

.PHONY: unit
unit: ## Run only Go unit tests
	cd observer && go test ./...
	cd certsvc && go test ./...

.PHONY: schema-report
schema-report: ## Build+run the unrecognized-shape report over the object store (reads OBSERVER_S3_* env). Pass ARGS="--since <ns> --redact-keys".
	cd observer && go run ./cmd/schemareport $(ARGS)

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
