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
	@echo "Homebase — a home server you can actually manage"
	@echo
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "To see it working:  make run       (then open http://127.0.0.1:8080)"
	@echo "To check the tree:  make check go-test dash-lint"

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
check: hygiene lint validate docs-build go-lint go-check test-repo test-seed ## Run every check CI runs
	@echo
	@echo "All checks passed."

# The Go tests, without the race detector.
#
# `check` used to stop at gofmt and vet, which is how a commit went out with a
# failing test in it: the checks that ran all passed, and the one that would
# have caught it was a separate command nobody had reason to think of. A gate
# that does not cover what you changed is a gate you learn to trust wrongly.
#
# Without -race so this stays a few seconds rather than a minute; `make go-test`
# runs the full thing and so does CI.
.PHONY: go-check
go-check:
	@go test ./... > /dev/null || (go test ./...; exit 1)
	@echo "Go: tests pass."

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

# The release machinery, checked without a VM and without Homebase's own
# binaries: what is under test is the archive around them.
.PHONY: test-repo
test-repo: ## Check the archive verifier refuses the archives it should
	@python3 tests/unit/test_repo.py

# The one piece of shell in this project that can destroy a disk. Tested against
# invented device tables, with the destructive half replaced by an echo.
.PHONY: test-seed
test-seed: ## Check which disk the installer's seed would clear
	@python3 tests/unit/test_installer_seed.py

.PHONY: hostd-describe
hostd-describe: ## Print the privileged operation registry as JSON
	@go run ./cmd/hostd --describe

.PHONY: hostd-check-operations
hostd-check-operations: ## Check the destructive operations still require confirmation
	@python3 scripts/check_operations.py

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

# --- Running it --------------------------------------------------------------

.PHONY: run
run: go-build dash-build ## Run Homebase on this machine and open it in a browser
	@python3 scripts/run-local.py

.PHONY: run-fresh
run-fresh: go-build dash-build ## Same, but discard the existing account and state first
	@python3 scripts/run-local.py --fresh

# --- Packaging ---------------------------------------------------------------

.PHONY: packages
packages: go-build dash-build ## Build the Debian packages into dist/
	@python3 scripts/build-packages.py --version $(VERSION)
	@python3 scripts/build-sbom.py --version $(VERSION)

.PHONY: sbom
sbom: go-build ## Write the bills of materials for the built binaries
	@python3 scripts/build-sbom.py --version $(VERSION)

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

.PHONY: vm-test-network
vm-test-network: ## Two machines: reachable by name from the other one
	@python3 tests/vm/test_network.py

.PHONY: vm-test-packages
vm-test-packages: ## Install, upgrade and purge the .debs on a clean machine
	@python3 tests/vm/test_packages.py

.PHONY: vm-run
vm-run: ## Run Homebase on a throwaway VM and leave it running, to click around in
	@python3 scripts/vm-demo.py

.PHONY: vm-run-destroy
vm-run-destroy: ## Destroy the machine `make vm-run` created
	@python3 scripts/vm-demo.py --destroy

.PHONY: vm-test-installer
vm-test-installer: ## Install onto a Windows-occupied disk from the real ISO; ~15 min
	@python3 tests/installer/test_install.py

.PHONY: vm-test-backup
vm-test-backup: ## Back up one machine, destroy it, restore onto a different one
	@python3 tests/vm/test_backup.py

.PHONY: vm-test-storage
vm-test-storage: ## Add a real USB disk, unplug it, reconnect it; nothing may corrupt
	@python3 tests/vm/test_storage.py

.PHONY: vm-test-update
vm-test-update: ## Update from a signed archive, and refuse one that was tampered with
	@python3 tests/vm/test_update.py

.PHONY: vm-test-vpn
vm-test-vpn: ## Reach the server from another machine over WireGuard
	@python3 tests/vm/test_vpn.py

.PHONY: vm-test-secureboot
vm-test-secureboot: ## Boot and run with Secure Boot enforcing, as laptops ship
	@python3 tests/vm/test_secureboot.py

.PHONY: vm-test-wifi
vm-test-wifi: ## Join a simulated wireless network; a wrong password must cost nothing
	@python3 tests/vm/test_wifi.py

.PHONY: vm-test-apps
vm-test-apps: ## Install an application, use it, reboot, uninstall; data must survive
	@python3 tests/vm/test_apps.py

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
