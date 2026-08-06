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
