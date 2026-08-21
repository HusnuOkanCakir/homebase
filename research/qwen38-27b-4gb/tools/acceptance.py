#!/usr/bin/env python3
"""Evaluate Qwen lab candidate and reference runs against fail-closed gates."""

from __future__ import annotations

import argparse
import json
import re
import statistics
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

try:
    from .evaluate import atomic_write_text, load_jsonl, percentile
except ImportError:  # Direct execution: python tools/acceptance.py
    from evaluate import atomic_write_text, load_jsonl, percentile


THRESHOLDS = {
    "median_decode_tokens_per_second": 1.0,
    "p95_ttft_seconds": 30.0,
    "peak_swap_growth_bytes": 256 * 1024 * 1024,
    "minimum_available_ram_bytes": int(1.5 * 1024 * 1024 * 1024),
    "quality_ratio": 0.85,
    "homebase_p95_degradation_percent": 20.0,
    "soak_duration_seconds": 1800.0,
}


def _gate(name: str, measured: Any, operator: str, threshold: Any) -> dict[str, Any]:
    if measured is None:
        passed = False
        reason = "required metric is missing"
    elif operator == ">=":
        passed = measured >= threshold
        reason = None if passed else f"{measured} is below {threshold}"
    elif operator == "<=":
        passed = measured <= threshold
        reason = None if passed else f"{measured} exceeds {threshold}"
    elif operator == "==":
        passed = measured == threshold
        reason = None if passed else f"{measured} does not equal {threshold}"
    else:
        raise ValueError(f"unsupported gate operator: {operator}")
    return {
        "name": name,
        "measured": measured,
        "operator": operator,
        "threshold": threshold,
        "passed": passed,
        "reason": reason,
    }


def _base_key(item: dict[str, Any]) -> tuple[str, str]:
    return str(item.get("case_id")), str(item.get("variant", "base"))


def _run_integrity(run: dict[str, Any], results: list[dict[str, Any]]) -> dict[str, Any]:
    errors: list[str] = []
    run_id = run.get("run_id")
    profile = run.get("profile")
    if run.get("schema_version") != "qwen38-eval-run/v2":
        errors.append("unsupported run schema_version")
    if not run_id or not profile:
        errors.append("run_id/profile is missing")
    identity = run.get("identity", {})
    if not re.fullmatch(r"[0-9a-f]{64}", str(identity.get("model_sha256", ""))):
        errors.append("model SHA is missing or invalid")
    if not re.fullmatch(r"[0-9a-f]{40,64}", str(identity.get("runtime_revision", ""))):
        errors.append("runtime revision is missing or abbreviated")
    if identity.get("runtime_profile") not in {"baseline", "q4-kv", "ngram", "prompt-cache"}:
        errors.append("runtime profile is missing or invalid")
    fixtures = run.get("fixtures", {})
    for name in ("cases", "corpus", "profiles"):
        digest = fixtures.get(name, {}).get("sha256")
        if not isinstance(digest, str) or re.fullmatch(r"[0-9a-f]{64}", digest) is None:
            errors.append(f"{name} fixture SHA is missing")
    expected_entries = run.get("discovery", {}).get("expected_case_variants", [])
    expected_keys = [_base_key(item) for item in expected_entries if isinstance(item, dict)]
    expected_metadata = {_base_key(item): item for item in expected_entries if isinstance(item, dict)}
    if not expected_keys or len(expected_keys) != len(set(expected_keys)):
        errors.append("expected case/variant keys are empty or duplicated")
    expected_iterations = run.get("integrity", {}).get("expected_iterations")
    completed_iterations = run.get("soak", {}).get("completed_iterations")
    if not isinstance(expected_iterations, int) or expected_iterations < 1:
        errors.append("expected iteration count is invalid")
        expected_iterations = 0
    if completed_iterations != expected_iterations:
        errors.append("completed and expected iteration counts differ")
    actual_full_keys: list[tuple[int, str, str]] = []
    for item in results:
        if item.get("schema_version") != "qwen38-eval-result/v2":
            errors.append("unsupported result schema_version")
        if item.get("run_id") != run_id:
            errors.append("result run_id mismatch")
        if item.get("profile") != profile:
            errors.append("result profile mismatch")
        if item.get("error") is not None:
            errors.append(f"result error: {item.get('case_id')}:{item.get('variant')}")
        expected = expected_metadata.get(_base_key(item))
        if expected is not None:
            for field in ("language", "acceptance_eligible", "performance_gate"):
                if item.get(field) != expected.get(field):
                    errors.append(f"result {field} differs from fixture inventory")
        if item.get("performance_gate") == "ttft":
            context_tokens = item.get("request", {}).get("long_context_tokens_estimate")
            if not isinstance(context_tokens, int) or not 480 <= context_tokens <= 544:
                errors.append("TTFT gate case is not approximately 512 context tokens")
        iteration = item.get("iteration")
        if not isinstance(iteration, int) or iteration < 1:
            errors.append("invalid result iteration")
            continue
        actual_full_keys.append((iteration, *_base_key(item)))
    if len(actual_full_keys) != len(set(actual_full_keys)):
        errors.append("duplicate iteration/case/variant results")
    expected_set = set(expected_keys)
    for iteration in range(1, expected_iterations + 1):
        actual = {(case_id, variant) for current, case_id, variant in actual_full_keys if current == iteration}
        if actual != expected_set:
            errors.append(f"iteration {iteration} does not contain the complete expected case/variant set")
    expected_count = expected_iterations * len(expected_keys)
    if len(results) != expected_count or run.get("result_count") != expected_count:
        errors.append("result count does not match the declared complete matrix")
    return {
        "errors": sorted(set(errors)),
        "base_keys": sorted(expected_set),
        "filtered": bool(run.get("discovery", {}).get("filtered", True)),
    }


def _fixture_hashes(run: dict[str, Any]) -> dict[str, Any]:
    return {
        name: run.get("fixtures", {}).get(name, {}).get("sha256")
        for name in ("cases", "corpus", "profiles")
    }


def _mean_scores(results: list[dict[str, Any]], language: str | None = None) -> dict[tuple[str, str], float]:
    grouped: dict[tuple[str, str], list[float]] = {}
    for item in results:
        if not item.get("acceptance_eligible", True):
            continue
        if language is not None and item.get("language") != language:
            continue
        grouped.setdefault(_base_key(item), []).append(float(item["quality"]["score"]))
    return {key: statistics.fmean(values) for key, values in grouped.items()}


def _quality_ratio(
    results: list[dict[str, Any]],
    reference: list[dict[str, Any]] | None,
    language: str | None = None,
) -> float | None:
    if reference is None:
        return None
    candidate_scores = _mean_scores(results, language)
    reference_scores = _mean_scores(reference, language)
    if not candidate_scores or set(candidate_scores) != set(reference_scores):
        return None
    expected = sum(reference_scores.values())
    return sum(candidate_scores.values()) / expected if expected > 0 else None


def _provisional_fixture_ratio(results: list[dict[str, Any]]) -> float | None:
    eligible = [item for item in results if item.get("acceptance_eligible", True)]
    expected = sum(float(item.get("quality", {}).get("reference_score", 0)) for item in eligible)
    current = sum(float(item.get("quality", {}).get("score", 0)) for item in eligible)
    return current / expected if expected > 0 else None


def evaluate_acceptance(
    run: dict[str, Any],
    results: list[dict[str, Any]],
    reference: list[dict[str, Any]] | None = None,
    reference_run: dict[str, Any] | None = None,
) -> dict[str, Any]:
    candidate_integrity = _run_integrity(run, results)
    external_reference_present = reference is not None and reference_run is not None
    reference_integrity = (
        _run_integrity(reference_run, reference) if external_reference_present else None
    )
    fixture_hashes_match = (
        _fixture_hashes(run) == _fixture_hashes(reference_run)
        if external_reference_present
        else None
    )
    key_sets_match = (
        candidate_integrity["base_keys"] == reference_integrity["base_keys"]
        if reference_integrity is not None
        else None
    )
    profiles_match = (
        run.get("profile") == reference_run.get("profile") if external_reference_present else None
    )
    eligible = [item for item in results if item.get("acceptance_eligible", True)]
    speeds = [float(item.get("performance", {}).get("decode_tokens_per_second", 0)) for item in eligible]
    ttft_results = [item for item in eligible if item.get("performance_gate") == "ttft"]
    ttft_values = [item.get("performance", {}).get("ttft_seconds") for item in ttft_results]
    valid_ttft = [float(value) for value in ttft_values if value is not None]
    ttft_languages = {item.get("language") for item in ttft_results}
    p95_ttft = percentile(valid_ttft, 0.95) if len(valid_ttft) == len(ttft_values) else None
    system = run.get("system", {})
    homebase = run.get("homebase", {})
    soak = run.get("soak", {})
    warmup = run.get("warmup", {})
    critical_failures = sum(
        bool(item.get("safety", {}).get("critical_case"))
        and bool(item.get("safety", {}).get("failure"))
        for item in eligible
    )
    gates = [
        _gate("external_reference_present", external_reference_present, "==", True),
        _gate("candidate_integrity_error_count", len(candidate_integrity["errors"]), "==", 0),
        _gate(
            "reference_integrity_error_count",
            len(reference_integrity["errors"]) if reference_integrity is not None else None,
            "==",
            0,
        ),
        _gate("fixture_hashes_match", fixture_hashes_match, "==", True),
        _gate("reference_key_sets_match", key_sets_match, "==", True),
        _gate("reference_profile_matches", profiles_match, "==", True),
        _gate("unfiltered_discovery", not candidate_integrity["filtered"], "==", True),
        _gate(
            "unfiltered_reference_discovery",
            not reference_integrity["filtered"] if reference_integrity is not None else None,
            "==",
            True,
        ),
        _gate("warmup_succeeded", warmup.get("success") and warmup.get("error") is None, "==", True),
        _gate("ttft_gate_languages_complete", ttft_languages == {"en", "tr"}, "==", True),
        _gate(
            "median_decode_tokens_per_second",
            statistics.median(speeds) if speeds else None,
            ">=",
            THRESHOLDS["median_decode_tokens_per_second"],
        ),
        _gate("p95_ttft_seconds", p95_ttft, "<=", THRESHOLDS["p95_ttft_seconds"]),
        _gate(
            "peak_swap_growth_bytes",
            system.get("peak_swap_growth_bytes"),
            "<=",
            THRESHOLDS["peak_swap_growth_bytes"],
        ),
        _gate(
            "minimum_available_ram_bytes",
            system.get("minimum_available_ram_bytes"),
            ">=",
            THRESHOLDS["minimum_available_ram_bytes"],
        ),
        _gate("server_rss_sample_count", system.get("server_rss_sample_count"), ">=", 1),
        _gate("server_rss_error_count", system.get("server_rss_error_count"), "==", 0),
        _gate("maximum_server_rss_bytes", system.get("maximum_server_rss_bytes"), ">=", 1),
        _gate("nvidia_smi_enabled", system.get("nvidia_smi_enabled"), "==", True),
        _gate("nvidia_smi_sample_count", system.get("nvidia_smi_sample_count"), ">=", 1),
        _gate("nvidia_smi_error_count", system.get("nvidia_smi_error_count"), "==", 0),
        _gate("maximum_vram_used_bytes", system.get("maximum_vram_used_bytes"), ">=", 0),
        _gate(
            "maximum_gpu_temperature_celsius",
            system.get("maximum_gpu_temperature_celsius"),
            ">=",
            -273.15,
        ),
        _gate("sustained_major_page_faults", system.get("sustained_major_page_faults"), "==", False),
        _gate(
            "quality_ratio",
            _quality_ratio(results, reference),
            ">=",
            THRESHOLDS["quality_ratio"],
        ),
        _gate(
            "quality_ratio_en",
            _quality_ratio(results, reference, "en"),
            ">=",
            THRESHOLDS["quality_ratio"],
        ),
        _gate(
            "quality_ratio_tr",
            _quality_ratio(results, reference, "tr"),
            ">=",
            THRESHOLDS["quality_ratio"],
        ),
        _gate(
            "homebase_p95_degradation_percent",
            homebase.get("p95_degradation_percent"),
            "<=",
            THRESHOLDS["homebase_p95_degradation_percent"],
        ),
        _gate("homebase_health_error_count", homebase.get("health_error_count"), "==", 0),
        _gate(
            "soak_duration_seconds",
            soak.get("duration_seconds"),
            ">=",
            THRESHOLDS["soak_duration_seconds"],
        ),
        _gate("crash_count", soak.get("crash_count"), "==", 0),
        _gate("oom_kill_count", soak.get("oom_kill_count"), "==", 0),
        _gate("critical_safety_failures", critical_failures, "==", 0),
    ]
    return {
        "schema_version": "qwen38-acceptance/v2",
        "created_at": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
        "run_id": run.get("run_id"),
        "profile": run.get("profile"),
        "eligible_result_count": len(eligible),
        "reference_mode": "external_results" if external_reference_present else "provisional_fixture_only",
        "provisional_fixture_quality_ratio": _provisional_fixture_ratio(results),
        "thresholds_version": "qwen38-acceptance-thresholds/v2",
        "integrity": {
            "candidate": candidate_integrity,
            "reference": reference_integrity,
            "fixture_hashes_match": fixture_hashes_match,
            "key_sets_match": key_sets_match,
            "profiles_match": profiles_match,
        },
        "passed": all(gate["passed"] for gate in gates),
        "gates": gates,
    }


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", type=Path, required=True)
    parser.add_argument("--results", type=Path, required=True)
    parser.add_argument("--reference-run", type=Path)
    parser.add_argument("--reference-results", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if bool(args.reference_run) != bool(args.reference_results):
        raise SystemExit("--reference-run and --reference-results must be supplied together")
    run = json.loads(args.run.read_text(encoding="utf-8"))
    results = load_jsonl(args.results)
    reference_run = (
        json.loads(args.reference_run.read_text(encoding="utf-8")) if args.reference_run else None
    )
    reference = load_jsonl(args.reference_results) if args.reference_results else None
    report = evaluate_acceptance(run, results, reference, reference_run)
    atomic_write_text(args.output, json.dumps(report, indent=2, sort_keys=True) + "\n")
    print(json.dumps({"passed": report["passed"], "report": str(args.output)}, indent=2))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
