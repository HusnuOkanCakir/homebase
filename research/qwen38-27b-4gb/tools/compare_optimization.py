#!/usr/bin/env python3
"""Fail-closed A/B retention gate for Qwen runtime optimizations."""

from __future__ import annotations

import argparse
import json
import statistics
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

try:
    from .acceptance import _fixture_hashes, _gate, _run_integrity
    from .evaluate import atomic_write_text, load_jsonl
except ImportError:  # Direct execution: python tools/compare_optimization.py
    from acceptance import _fixture_hashes, _gate, _run_integrity
    from evaluate import atomic_write_text, load_jsonl


OPTIMIZED_PROFILES = {"q4-kv", "ngram", "prompt-cache"}


def _case_metrics(results: list[dict[str, Any]]) -> tuple[dict[tuple[str, str], dict[str, float]], list[str]]:
    grouped: dict[tuple[str, str], list[dict[str, Any]]] = {}
    errors = []
    for item in results:
        key = (str(item.get("case_id")), str(item.get("variant", "base")))
        grouped.setdefault(key, []).append(item)
    metrics = {}
    for key, items in grouped.items():
        try:
            completion_tokens = sum(float(item["performance"]["completion_tokens"]) for item in items)
            total_seconds = sum(float(item["performance"]["total_seconds"]) for item in items)
            quality = statistics.fmean(float(item["quality"]["score"]) for item in items)
        except (KeyError, TypeError, ValueError, statistics.StatisticsError):
            errors.append(f"invalid metric fields for {key[0]}:{key[1]}")
            continue
        if completion_tokens < 0 or total_seconds <= 0:
            errors.append(f"non-positive end-to-end metric for {key[0]}:{key[1]}")
            continue
        metrics[key] = {
            "completion_tokens": completion_tokens,
            "total_seconds": total_seconds,
            "throughput_tokens_per_second": completion_tokens / total_seconds,
            "quality_score": quality,
        }
    return metrics, errors


def compare_optimization(
    baseline_run: dict[str, Any],
    baseline_results: list[dict[str, Any]],
    candidate_run: dict[str, Any],
    candidate_results: list[dict[str, Any]],
) -> dict[str, Any]:
    baseline_integrity = _run_integrity(baseline_run, baseline_results)
    candidate_integrity = _run_integrity(candidate_run, candidate_results)
    baseline_identity = baseline_run.get("identity", {})
    candidate_identity = candidate_run.get("identity", {})
    model_match = baseline_identity.get("model_sha256") == candidate_identity.get("model_sha256")
    revision_match = baseline_identity.get("runtime_revision") == candidate_identity.get("runtime_revision")
    profile_match = baseline_run.get("profile") == candidate_run.get("profile")
    fixtures_match = _fixture_hashes(baseline_run) == _fixture_hashes(candidate_run)
    keys_match = baseline_integrity["base_keys"] == candidate_integrity["base_keys"]
    baseline_metrics, baseline_metric_errors = _case_metrics(baseline_results)
    candidate_metrics, candidate_metric_errors = _case_metrics(candidate_results)
    metric_errors = baseline_metric_errors + candidate_metric_errors
    if set(baseline_metrics) != set(candidate_metrics):
        metric_errors.append("paired metric key sets differ")
    pairs = []
    quality_regressions = []
    for key in sorted(set(baseline_metrics) & set(candidate_metrics)):
        baseline = baseline_metrics[key]
        candidate = candidate_metrics[key]
        improvement = (
            candidate["throughput_tokens_per_second"]
            / baseline["throughput_tokens_per_second"]
            - 1
        ) * 100 if baseline["throughput_tokens_per_second"] > 0 else None
        quality_delta = candidate["quality_score"] - baseline["quality_score"]
        if quality_delta < -1e-12:
            quality_regressions.append(f"{key[0]}:{key[1]}")
        pairs.append(
            {
                "case_id": key[0],
                "variant": key[1],
                "baseline": baseline,
                "candidate": candidate,
                "throughput_improvement_percent": improvement,
                "quality_delta": quality_delta,
            }
        )
    baseline_tokens = sum(metric["completion_tokens"] for metric in baseline_metrics.values())
    baseline_seconds = sum(metric["total_seconds"] for metric in baseline_metrics.values())
    candidate_tokens = sum(metric["completion_tokens"] for metric in candidate_metrics.values())
    candidate_seconds = sum(metric["total_seconds"] for metric in candidate_metrics.values())
    baseline_throughput = baseline_tokens / baseline_seconds if baseline_seconds > 0 else None
    candidate_throughput = candidate_tokens / candidate_seconds if candidate_seconds > 0 else None
    improvement = (
        (candidate_throughput / baseline_throughput - 1) * 100
        if baseline_throughput and candidate_throughput is not None
        else None
    )
    gates = [
        _gate("baseline_integrity_error_count", len(baseline_integrity["errors"]), "==", 0),
        _gate("candidate_integrity_error_count", len(candidate_integrity["errors"]), "==", 0),
        _gate("baseline_unfiltered", not baseline_integrity["filtered"], "==", True),
        _gate("candidate_unfiltered", not candidate_integrity["filtered"], "==", True),
        _gate("model_sha256_matches", model_match, "==", True),
        _gate("runtime_revision_matches", revision_match, "==", True),
        _gate("evaluation_profile_matches", profile_match, "==", True),
        _gate("fixture_hashes_match", fixtures_match, "==", True),
        _gate("complete_case_variant_keys_match", keys_match, "==", True),
        _gate("baseline_runtime_profile", baseline_identity.get("runtime_profile"), "==", "baseline"),
        _gate(
            "candidate_runtime_profile_is_optimized",
            candidate_identity.get("runtime_profile") in OPTIMIZED_PROFILES,
            "==",
            True,
        ),
        _gate("metric_error_count", len(metric_errors), "==", 0),
        _gate("throughput_improvement_percent", improvement, ">=", 10.0),
        _gate("quality_regression_count", len(quality_regressions), "==", 0),
    ]
    return {
        "schema_version": "qwen38-optimization-comparison/v1",
        "created_at": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
        "baseline_run_id": baseline_run.get("run_id"),
        "candidate_run_id": candidate_run.get("run_id"),
        "baseline_runtime_profile": baseline_identity.get("runtime_profile"),
        "candidate_runtime_profile": candidate_identity.get("runtime_profile"),
        "integrity": {
            "baseline_errors": baseline_integrity["errors"],
            "candidate_errors": candidate_integrity["errors"],
            "metric_errors": metric_errors,
        },
        "throughput": {
            "basis": "sum(completion_tokens) / sum(total_seconds)",
            "baseline_tokens_per_second": baseline_throughput,
            "candidate_tokens_per_second": candidate_throughput,
            "improvement_percent": improvement,
        },
        "quality": {
            "policy": "no paired case/variant mean score may regress",
            "regression_count": len(quality_regressions),
            "regressions": quality_regressions,
        },
        "pairs": pairs,
        "passed": all(gate["passed"] for gate in gates),
        "gates": gates,
    }


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline-run", type=Path, required=True)
    parser.add_argument("--baseline-results", type=Path, required=True)
    parser.add_argument("--candidate-run", type=Path, required=True)
    parser.add_argument("--candidate-results", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    report = compare_optimization(
        json.loads(args.baseline_run.read_text(encoding="utf-8")),
        load_jsonl(args.baseline_results),
        json.loads(args.candidate_run.read_text(encoding="utf-8")),
        load_jsonl(args.candidate_results),
    )
    atomic_write_text(args.output, json.dumps(report, indent=2, sort_keys=True) + "\n")
    print(json.dumps({"passed": report["passed"], "report": str(args.output)}, indent=2))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
