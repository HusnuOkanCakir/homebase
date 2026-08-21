# Claude handoff: Qwen3.8-27B on a 4 GB Pascal host

This file is the continuation brief for an agent working on this research lab.
Read it before changing anything under this directory. Then read
[`README.md`](README.md), which is the operator runbook.

## Mission and scope

The goal is to determine honestly whether the official Qwen3.8-27B text model
can provide a useful single-user assistant on this assumed host:

- x86-64 Linux/Homebase
- NVIDIA Pascal GPU with approximately 4 GB VRAM
- 16 GB system RAM
- approximately 900 GB HDD
- target hot decode speed of at least 1 token/second

This is an isolated research project, not a Homebase feature. Keep all source
changes under `research/qwen38-27b-4gb/`. Do not change `cmd/`, `internal/`,
`dashboard/`, `schemas/`, `installer/`, packaging, app manifests, or backup
logic as part of this lab.

Large data belongs under `/srv/qwen-lab`, never in the repository or Homebase
application data. GGUF files, safetensors, checkpoints, caches, raw runs, and
logs must remain untracked.

## Current technical conclusion

Qwen3.8-27B is not Qwen3-8B. Its text backbone is dense rather than MoE: 64
layers arranged as 48 Gated DeltaNet and 16 full-attention layers. Expert
offload therefore does not apply.

The full model cannot fit in 4 GB VRAM. Even an ideal metadata-free 1.58-bit
weight budget is about 5.33 GB. A theoretical 1-bit budget leaves no room for
scales, GDN state, KV cache, or CUDA workspace. The practical near-term path
is a low-bit GGUF held mostly in host RAM with limited GPU offload through
`llama.cpp`.

The long-term approximately 9.9B full-VRAM student remains a research
hypothesis only. See [`notes/CANDIDATE_A.md`](notes/CANDIDATE_A.md). Do not
claim that this checkpoint exists or can be trained on the target host.

## What has been implemented

- Artifact supply chain: immutable Hugging Face revisions, byte sizes, and
  SHA-256 values are in
  [`config/models.lock.json`](config/models.lock.json). Requantized inputs are
  rejected.

- Hardware probe: CPU model/flags/physical cores, RAM/swap, storage, NVIDIA
  identity/VRAM/compute capability/driver/temperature, CUDA, and sensors.

- Runtime build: `llama.cpp` b10549 at full commit
  `b2e5e9b28b2484fbf94b543432ece638996a8b97`, CUDA 12.9, `sm_60` and `sm_61`,
  host-native CPU optimization, and shared-library attestation.

- GDN safety gate: a locked Qwen3.5-0.8B GGUF runs through separate ephemeral
  loopback `llama-server` processes, first CPU and then CUDA. Native
  `/completion` outputs and token IDs must agree before 27B is approved.

- Baseline service: text-only, offline, no vision projector, loopback
  `127.0.0.1:8088`, context 2048, parallel 1, Q8 KV, and GPU auto-fit with a
  1024 MiB target reserve.

- Benchmark matrix: 2K/4K context, physical/logical threads, ubatch 64/128,
  Q8/Q4 KV, CPU/GPU KV, FA on/off, and auto or explicit GPU-layer counts.

- Memory rejection: cases sample RSS, VRAM, available RAM, swap, process swap,
  major faults, and swap-in after warm-up. Thrashing becomes
  `memory_eliminated`; expected OOM and timeouts do not discard later cases.

- Evaluation: fixed English/Turkish chat, summary, reasoning, instruction,
  factual, 2K/4K recall, RAG, and prompt-injection cases.

- Acceptance: requires a real external reference, 30-minute soak, complete
  unfiltered fixtures, runtime/model hashes, RSS/GPU telemetry, Homebase health
  probes, and all numeric gates. Fixture scores alone cannot produce PASS.

- Optimizations: Q4 KV, draft-free n-gram speculation, and prompt-cache.
  [`tools/compare_optimization.py`](tools/compare_optimization.py) requires at
  least 10% paired end-to-end gain and no task-level quality regression.

- Uncensored models: allowed only after the official baseline and only with a
  strict, externally created read-only/networkless/tool-free/no-host-mount
  sandbox attestation. The evaluator verifies evidence; it creates no sandbox.

- Colab: output-free, resumable, and zero-spend. The default runs a toy
  surrogate only. Q4 requires explicit opt-in, sufficient memory/disk, exact
  checksum, and an attested local runtime.

The main implementation entry point is [`bin/qwen-lab`](bin/qwen-lab). Its
dependency-free implementation and tests are in `lib/`. Evaluation tools and
their versioned schemas are under `tools/`, `tests/`, and `eval/`.

## Immutable inputs

Treat the lock files as authoritative; do not replace full revisions with
`main` or abbreviated hashes.

- Runtime: `llama.cpp` b10549 / `b2e5e9b28b2484fbf94b543432ece638996a8b97`
- Sanity model: `qwen35-0.8b-ggml-q4-0-sanity`
- First correctness baseline: `qwen38-27b-unsloth-ud-iq2-m`
- Then: `qwen38-27b-unsloth-ud-iq2-xxs`
- Then: `qwen38-27b-unsloth-ud-iq3-xxs`
- Then: `qwen38-27b-unsloth-q3-k-m`
- High-memory reference only: `qwen38-27b-ggml-q4-k-m-reference`

The official source entry currently locks its safetensors index, not all 18
BF16 shards. Before any future conversion, pruning, or student work, create a
separate conversion-input lock containing every shard's byte size and SHA-256.

## Required execution order on the target host

Run from this directory. The tooling never installs packages or buys compute.

```bash
make check

./bin/qwen-lab --data-dir /srv/qwen-lab probe \
  --output /srv/qwen-lab/results/hardware.json
./bin/qwen-lab --data-dir /srv/qwen-lab doctor \
  --output /srv/qwen-lab/results/doctor.json

./bin/qwen-lab --data-dir /srv/qwen-lab build --dry-run
./bin/qwen-lab --data-dir /srv/qwen-lab build

./bin/qwen-lab --data-dir /srv/qwen-lab fetch \
  qwen35-0.8b-ggml-q4-0-sanity
./bin/qwen-lab --data-dir /srv/qwen-lab sanity --approve

./bin/qwen-lab --data-dir /srv/qwen-lab fetch \
  qwen38-27b-unsloth-ud-iq2-m
./bin/qwen-lab --data-dir /srv/qwen-lab bench \
  qwen38-27b-unsloth-ud-iq2-m --case 0
```

Do not download or run a 27B artifact until `sanity --approve` succeeds on the
actual Pascal host. Start with the UD-IQ2_M CPU-only case for the correctness
baseline, then CUDA auto-offload. Proceed in the locked model order above.
Pass 2K gates before attempting 4K.

After a stable server exists, follow [`eval/README.md`](eval/README.md) for the
diagnostic run, 30-minute soak, Q4/best-accessible reference comparison, RAG
comparison, and optimization A/B gate.

## Fixed go/no-go gates

A usable candidate must satisfy all of these in one reproducible setup:

- median warm decode at least 1 token/second
- tagged approximately 512-token prompt p95 TTFT at most 30 seconds
- 30 minutes without crash or OOM
- post-warm-up swap growth at most 256 MiB
- at least 1.5 GiB available host RAM
- no sustained major-page-fault or swap-in activity
- English quality at least 85% of the real reference
- Turkish quality at least 85% of the real reference
- no critical prompt-injection safety failure
- Homebase health error count zero
- Homebase API p95 degradation at most 20%
- populated, error-free peak server RSS, VRAM, and GPU-temperature telemetry

If Q2 fails quality and Q3 cannot pass memory/speed, record the conclusion as:

> Qwen3.8-27B is not a usable service on this 16+4 GB host.

Do not hide that result with disk swap or per-token HDD weight streaming.

## Verified state versus unknown state

Verified in the development workspace:

- 44 lab unit/contract tests passed.
- Lab structure, locks, JSON/JSONL, JSON Schemas, notebook cells, and CLI
  dry-runs passed.
- Repository-wide `make check` passed, including Markdown, contracts, docs,
  Go format/vet/tests, archive tests, and installer tests.
- The clean research branch changes only this research directory.

Not yet verified:

- the target machine's exact GPU, CPU, RAM, swap, HDD speed, and thermals
- the pinned CUDA build on the real Pascal host
- an actual sanity-model CPU/CUDA run
- any Qwen3.8-27B download, inference, throughput, quality, or soak result
- an actual Q4 reference evaluation
- any student pruning, distillation, QAT, or checkpoint

Never convert a dry-run, unit test, paper result, or Colab toy surrogate into a
hardware-performance claim.

## Git and collaboration state

The recommended isolated branch before this handoff was
`perf/qwen38-27b-4gb-lab-only` at implementation commit `2e8380c`, based on the
then-current `main` commit `405cb9e`. Its diff contained only this directory.

The separately existing `perf/qwen38-27b-4gb-lab` branch also contains the
user's unrelated graphics commit `1c50fd4`; it was deliberately preserved and
must not be rewritten or reset. The lab completion on that branch was
`56161da`.

Before making a new change:

1. Check the current branch and working tree.
2. Preserve unrelated user edits and commits.
3. Keep the lab-only branch's diff restricted to this directory.
4. Run `make check` here and the repository-level `make check` before handoff.
5. Use Conventional Commits and include the required DCO sign-off.

## Research references

The rationale and primary links are in
[`notes/PAPER_DECISIONS.md`](notes/PAPER_DECISIONS.md). Integration blockers
and the no-shell/no-`hostd` security boundary are in
[`notes/HANDOFF.md`](notes/HANDOFF.md).
