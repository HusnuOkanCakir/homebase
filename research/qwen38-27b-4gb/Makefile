PYTHON ?= python3
LAB_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))

.PHONY: check validate unit smoke shell quant-sweep

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
	@$(PYTHON) "$(LAB_ROOT)/tools/quant_sweep.py" --help >/dev/null
	@echo "Qwen lab: CLI smoke checks passed."

# The active track. Needs a llama-bench and the models, so it is not part of
# `check` — it measures hardware rather than checking code.
#
#   make quant-sweep BENCH=/srv/qwen-lab/runtime/cpu/llama-b10549/llama-bench \
#        PEAK_GB_S=25.6 MODELS="/srv/qwen-lab/models/candidates/*.gguf"
quant-sweep:
	@test -n "$(BENCH)" || { echo "set BENCH=/path/to/llama-bench"; exit 1; }
	@test -n "$(PEAK_GB_S)" || { echo "set PEAK_GB_S=<memory bandwidth in GB/s>"; exit 1; }
	@$(PYTHON) "$(LAB_ROOT)/tools/quant_sweep.py" \
		--bench "$(BENCH)" --peak-gb-s "$(PEAK_GB_S)" \
		--out "$(LAB_ROOT)/results/quant-curve.json" $(MODELS)
