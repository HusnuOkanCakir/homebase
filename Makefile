# Homebase — development tasks.
#
# `make check` runs exactly what CI runs, at the same pinned versions. If the two
# ever disagree, that is a bug to be fixed here, not worked around.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

VENV    := .venv
BIN     := $(VENV)/bin
PYTHON  := $(BIN)/python
STAMP   := $(VENV)/.installed

# Directories containing hand-written YAML we control.
YAML_PATHS := .github schemas app-store mkdocs.yml

.PHONY: help
help: ## Show this help
	@echo "Homebase — Milestone 0 (contracts and project machinery)"
	@echo
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "Milestone 0 needs only Python 3.11+ and Git."

# --- Environment -------------------------------------------------------------

.PHONY: bootstrap
bootstrap: $(STAMP) ## Create .venv and install pinned tooling

$(STAMP): requirements-dev.txt
	@test -d $(VENV) || python3 -m venv $(VENV)
	@$(BIN)/pip install --quiet --upgrade pip
	@$(BIN)/pip install --quiet -r requirements-dev.txt
	@touch $@
	@echo "Tooling installed in $(VENV)."

# --- The full check ----------------------------------------------------------

.PHONY: check
check: hygiene lint validate docs-build ## Run every check CI runs
	@echo
	@echo "All checks passed."

# --- Individual checks -------------------------------------------------------

.PHONY: hygiene
hygiene: ## Encoding, line endings, trailing whitespace, file size
	@python3 scripts/check_hygiene.py

.PHONY: lint
lint: lint-md lint-yaml lint-workflows ## Markdown, YAML and workflow linting

.PHONY: lint-md
lint-md: $(STAMP) ## Lint Markdown
	@$(BIN)/pymarkdown --config .pymarkdown.json scan .

.PHONY: lint-yaml
lint-yaml: $(STAMP) ## Lint YAML
	@$(BIN)/yamllint --strict $(wildcard $(YAML_PATHS))

.PHONY: lint-workflows
lint-workflows: $(STAMP) ## Analyse workflow security (unpinned actions, injection)
	@if [ -d .github/workflows ]; then \
		$(BIN)/zizmor --persona=regular .github/workflows/; \
	else \
		echo "No workflows to analyse."; \
	fi

.PHONY: links
links: ## Verify internal Markdown links resolve
	@python3 scripts/check_links.py

# --- Contracts ---------------------------------------------------------------

.PHONY: validate
validate: links validate-openapi validate-schemas ## Validate links, API and schemas

.PHONY: validate-openapi
validate-openapi: $(STAMP) ## Validate the OpenAPI contract
	@if [ -f api/openapi.yaml ]; then \
		$(BIN)/openapi-spec-validator api/openapi.yaml; \
		echo "OpenAPI: api/openapi.yaml is valid."; \
	else \
		echo "OpenAPI: no contract yet (lands in the contracts PR)."; \
	fi

.PHONY: validate-schemas
validate-schemas: $(STAMP) ## Validate JSON Schemas and their fixtures
	@if [ -f scripts/validate_contracts.py ]; then \
		$(PYTHON) scripts/validate_contracts.py; \
	else \
		echo "Schemas: no contracts yet (land in the contracts PR)."; \
	fi

# --- Documentation -----------------------------------------------------------

.PHONY: docs
docs: $(STAMP) ## Serve the documentation site on :8000
	@$(BIN)/mkdocs serve

.PHONY: docs-build
docs-build: $(STAMP) ## Build the documentation site, warnings are errors
	@if [ -f mkdocs.yml ]; then \
		$(BIN)/mkdocs build --strict --quiet; \
		echo "Docs: site builds cleanly."; \
	else \
		echo "Docs: no mkdocs.yml yet (lands in the architecture PR)."; \
	fi

# --- Go --------------------------------------------------------------------

.PHONY: go-build
go-build: ## Build the Go binaries into bin/
	@mkdir -p bin
	@CGO_ENABLED=0 go build -trimpath -o bin/ ./cmd/...
	@echo "Built: $$(ls bin/)"

.PHONY: go-test
go-test: ## Run the Go tests with the race detector
	@go test ./... -race -cover

.PHONY: go-lint
go-lint: ## gofmt and go vet
	@unformatted=$$(gofmt -l ./cmd ./internal); \
	if [ -n "$$unformatted" ]; then \
		echo "Not gofmt-formatted:"; echo "$$unformatted"; \
		echo; echo "Run: gofmt -w ./cmd ./internal"; exit 1; \
	fi
	@go vet ./...
	@echo "Go: formatted and vetted."

.PHONY: hostd-describe
hostd-describe: ## Print the privileged operation registry as JSON
	@go run ./cmd/hostd --describe

# --- Dashboard ---------------------------------------------------------------

.PHONY: dash-install
dash-install: ## Install dashboard dependencies
	@cd dashboard && npm ci

.PHONY: dash-build
dash-build: ## Typecheck and build the dashboard
	@cd dashboard && npm run build

.PHONY: dash-lint
dash-lint: ## Lint and typecheck the dashboard
	@cd dashboard && npm run typecheck && npm run lint
	@echo "Dashboard: typechecked and linted."

.PHONY: dash-dev
dash-dev: ## Serve the dashboard on :5173, proxying the API to a running core
	@cd dashboard && npm run dev

# --- Packaging ---------------------------------------------------------------

.PHONY: packages
packages: go-build dash-build ## Build the Debian packages into dist/
	@python3 scripts/build-packages.py --version $(VERSION)

VERSION ?= 0.0.0~dev

# --- VM lab --------------------------------------------------------------------
# Disposable Ubuntu VMs, for testing what cannot be tested honestly anywhere else:
# systemd units, real disks, real reboots. Raw QEMU and cloud images, no libvirt
# and no root — see docs/decisions/0010-vm-lab-qemu-cloud-image.md.

VMCTL := tests/vm/vmctl.py

.PHONY: vm-create
vm-create: ## Create and boot a disposable VM
	@python3 $(VMCTL) create

.PHONY: vm-start
vm-start: ## Boot an existing VM
	@python3 $(VMCTL) start

.PHONY: vm-ssh
vm-ssh: ## Open a shell in the VM
	@python3 $(VMCTL) ssh

.PHONY: vm-reboot
vm-reboot: ## Reboot the VM and wait for it to come back
	@python3 $(VMCTL) reboot

.PHONY: vm-reset
vm-reset: ## Destroy and recreate the VM from the cached base image
	@python3 $(VMCTL) destroy
	@python3 $(VMCTL) create

.PHONY: vm-logs
vm-logs: ## Export serial console and guest journal
	@python3 $(VMCTL) logs

.PHONY: vm-status
vm-status: ## List VMs
	@python3 $(VMCTL) status

.PHONY: vm-test
vm-test: ## End-to-end: create, install a service, reboot, verify, export, destroy
	@python3 tests/vm/test_lifecycle.py

.PHONY: vm-test-hostd
vm-test-hostd: ## hostd under real systemd: socket permissions, sandbox, audit, reboot
	@python3 tests/vm/test_hostd.py

.PHONY: vm-test-core
vm-test-core: ## The vertical slice: setup, sign in, read system, reboot, job resolves
	@python3 tests/vm/test_core.py

.PHONY: vm-test-dashboard
vm-test-dashboard: ## The milestone's user journey, in a real browser against a real VM
	@python3 tests/vm/test_dashboard.py

.PHONY: vm-test-packages
vm-test-packages: ## Install, upgrade and purge the .debs on a clean machine
	@python3 tests/vm/test_packages.py

.PHONY: vm-destroy
vm-destroy: ## Destroy the VM and its overlay
	@python3 $(VMCTL) destroy

.PHONY: vm-destroy-all
vm-destroy-all: ## Destroy every VM
	@python3 $(VMCTL) destroy --all

# --- Housekeeping ------------------------------------------------------------

.PHONY: clean
clean: ## Remove build output
	@rm -rf site .pymarkdown_cache
	@find . -name __pycache__ -type d -prune -exec rm -rf {} +
	@echo "Cleaned."

.PHONY: distclean
distclean: clean ## Remove the virtualenv as well
	@rm -rf $(VENV)
	@echo "Removed $(VENV)."
