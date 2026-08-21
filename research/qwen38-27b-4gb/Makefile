PYTHON ?= python3
LAB_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))

.PHONY: check validate unit smoke shell

check: validate shell unit smoke

validate:
	@$(PYTHON) "$(LAB_ROOT)/check.py"

shell:
	@bash -n "$(LAB_ROOT)/bin/self-test"

unit:
	@PYTHONPATH="$(LAB_ROOT)/lib" $(PYTHON) -m unittest discover \
		-v -s "$(LAB_ROOT)/lib" -p 'test_*.py'
	@$(PYTHON) -m unittest discover -v -s "$(LAB_ROOT)/tests" -p 'test_*.py'

smoke:
	@$(PYTHON) "$(LAB_ROOT)/bin/qwen-lab" --help >/dev/null
	@$(PYTHON) "$(LAB_ROOT)/tools/evaluate.py" --help >/dev/null
	@$(PYTHON) "$(LAB_ROOT)/tools/retrieve.py" --help >/dev/null
	@$(PYTHON) "$(LAB_ROOT)/tools/acceptance.py" --help >/dev/null
	@$(PYTHON) "$(LAB_ROOT)/tools/compare_optimization.py" --help >/dev/null
	@echo "Qwen lab: CLI smoke checks passed."
