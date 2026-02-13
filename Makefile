SHELL := /bin/bash

GO ?= go
CLAWFARM_BIN ?= $(CURDIR)/clawfarm
INTEGRATION_IMAGE_REF ?= ubuntu:24.04

.PHONY: help build test integration integration-001 integration-001-run integration-002 integration-003 clean

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_.-]+:.*##/ {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build clawfarm binary
	$(GO) build -o $(CLAWFARM_BIN) ./cmd/clawfarm

test: ## Run Go unit tests
	$(GO) test ./...

integration-001: build ## Run integration 001 (no VM run)
	INTEGRATION_ENABLE_RUN=0 INTEGRATION_IMAGE_REF=$(INTEGRATION_IMAGE_REF) CLAWFARM_BIN=$(CLAWFARM_BIN) integration/001-basic.sh

integration-001-run: build ## Run integration 001 with full VM bring-up
	INTEGRATION_ENABLE_RUN=1 INTEGRATION_IMAGE_REF=$(INTEGRATION_IMAGE_REF) CLAWFARM_BIN=$(CLAWFARM_BIN) integration/001-basic.sh

integration-002: build ## Run integration 002 (cache reuse + instance image copy)
	INTEGRATION_IMAGE_REF=$(INTEGRATION_IMAGE_REF) CLAWFARM_BIN=$(CLAWFARM_BIN) integration/002-cache-instance-copy.sh

integration-003: build ## Run integration 003 (real VM: `new --run` over SSH + volume write)
	INTEGRATION_IMAGE_REF=$(INTEGRATION_IMAGE_REF) CLAWFARM_BIN=$(CLAWFARM_BIN) integration/003-new-run-ssh.sh

integration: integration-001 integration-002 ## Run default integration suite

clean: ## Remove local build and temp artifacts
	rm -f $(CLAWFARM_BIN)
	rm -rf .tmp
