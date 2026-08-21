# Qwen 3.8 evaluation MVP

This directory contains deterministic Turkish/English cases, a small offline RAG corpus, and versioned schemas. The tools
use only the Python standard library, bypass environment proxies, and accept only literal loopback IP endpoints.

Run evaluation with a caller-owned output directory:

```bash
python3 research/qwen38-27b-4gb/tools/evaluate.py \
  --endpoint http://127.0.0.1:8088/v1 \
  --model-sha256 <64-character-model-sha256> \
  --runtime-revision <full-40-to-64-character-runtime-revision> \
  --runtime-profile baseline \
  --homebase-url http://127.0.0.1:8080/api/v1/health \
  --soak-seconds 1800 \
  --server-pid <inference-server-pid> \
  --nvidia-smi-telemetry \
  --output-dir /tmp/qwen38-run
```

The uncensored profile additionally requires an orchestrator-produced `--sandbox-attestation` JSON file matching
`sandbox-attestation.schema.json` exactly. The evaluator checks process start ticks, network and mount namespace inodes,
and read-only root evidence against live `/proc` state. It also checks the asserted model hash and runtime revision. It
verifies supplied evidence; it does not create a sandbox or prove the attester itself trustworthy. Static profile labels
are never treated as isolation proof.

A discarded warm-up request runs before resource and latency measurement. With `--soak-seconds 0`, the selected suite
runs once. A positive value repeats the entire selected suite and tags every result with its full-suite iteration number
until the target elapsed time is reached. Optional PID and `nvidia-smi` sampling records server RSS, VRAM, and temperature.

Evaluate the run gates after a soak run:

```bash
python3 research/qwen38-27b-4gb/tools/acceptance.py \
  --run /tmp/qwen38-run/run.json \
  --results /tmp/qwen38-run/results.jsonl \
  --reference-run /tmp/qwen38-reference/run.json \
  --reference-results /tmp/qwen38-reference/results.jsonl \
  --output /tmp/qwen38-run/acceptance.json
```

Only the tagged fixed EN/TR 512-token cases contribute to the TTFT gate. Acceptance requires an unfiltered, complete run,
a successful warm-up, one run ID/profile, no duplicate or errored result, matching fixture hashes and case/variant keys,
and a real reference run plus its results. Fixture reference scores are reported only as provisional diagnostics and can
never produce PASS. Candidate and reference model hashes may differ, while their cases, corpus, and profiles must match.

The numeric gates require median decode throughput of at least 1 token/s, tagged-case p95 TTFT at most 30 seconds, swap
growth at most 256 MiB, at least 1.5 GiB available RAM, no sustained major page faults, aggregate plus per-language quality
of at least 85% of reference, Homebase p95 degradation at most 20% with no health errors, at least 30 minutes of observed
run duration, no crashes or OOM kills, and no critical safety failure.

Although `--server-pid` and `--nvidia-smi-telemetry` are optional for short diagnostic runs, final acceptance requires both.
It fails closed unless RSS and NVIDIA telemetry each have at least one successful sample, zero sampling errors, and populated
peak server RSS, peak VRAM use, and peak GPU temperature values.

`results.jsonl` contains one `qwen38-eval-result/v2` object per iteration and case variant. RAG summaries compare paired
quality, TTFT, and safety overall and by language. The selected chunks, hashes, scores, and citations remain in each RAG
record. Evaluation and acceptance outputs are atomically replaced only at caller-specified paths. Retrieval prints JSON to
stdout unless the caller supplies `--output`.

## Optimization retention

Every evaluation records one runtime profile: `baseline`, `q4-kv`, `ngram`, or `prompt-cache`. Compare a baseline and one
optimized run with:

```bash
python3 research/qwen38-27b-4gb/tools/compare_optimization.py \
  --baseline-run /tmp/qwen38-baseline/run.json \
  --baseline-results /tmp/qwen38-baseline/results.jsonl \
  --candidate-run /tmp/qwen38-q4-kv/run.json \
  --candidate-results /tmp/qwen38-q4-kv/results.jsonl \
  --output /tmp/qwen38-q4-kv/optimization-comparison.json
```

The A/B gate requires the same model SHA-256, full runtime revision, evaluation profile, fixture hashes, and complete
unfiltered case/variant keys. It rejects duplicate or errored results. End-to-end throughput is paired and calculated as
`sum(completion_tokens) / sum(total_seconds)`; retention requires at least 10% overall improvement and no mean quality
score regression in any paired case/variant. The report is written atomically.
