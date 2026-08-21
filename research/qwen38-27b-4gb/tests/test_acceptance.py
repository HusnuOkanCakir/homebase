from __future__ import annotations

import sys
import unittest
from copy import deepcopy
from pathlib import Path


LAB_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(LAB_ROOT))

from tools.acceptance import evaluate_acceptance  # noqa: E402


def result(run_id: str, case_id: str, language: str, score: float = 0.9) -> dict:
    return {
        "schema_version": "qwen38-eval-result/v2",
        "run_id": run_id,
        "iteration": 1,
        "case_id": case_id,
        "language": language,
        "variant": "base",
        "profile": "official",
        "performance_gate": "ttft",
        "request": {"long_context_tokens_estimate": 512},
        "acceptance_eligible": True,
        "error": None,
        "performance": {"decode_tokens_per_second": 1.4, "ttft_seconds": 22.0},
        "quality": {"score": score, "reference_score": 1.0},
        "safety": {"critical_case": case_id == "en-case", "failure": False},
    }


def run_metadata(run_id: str) -> dict:
    expected = [
        {
            "case_id": "en-case",
            "variant": "base",
            "language": "en",
            "acceptance_eligible": True,
            "performance_gate": "ttft",
        },
        {
            "case_id": "tr-case",
            "variant": "base",
            "language": "tr",
            "acceptance_eligible": True,
            "performance_gate": "ttft",
        },
    ]
    return {
        "schema_version": "qwen38-eval-run/v2",
        "run_id": run_id,
        "profile": "official",
        "identity": {
            "model_sha256": "a" * 64,
            "runtime_revision": "b" * 40,
            "runtime_profile": "baseline",
        },
        "fixtures": {
            name: {"sha256": character * 64}
            for name, character in (("cases", "c"), ("corpus", "d"), ("profiles", "e"))
        },
        "discovery": {"filtered": False, "expected_case_variants": expected},
        "warmup": {"success": True, "error": None},
        "result_count": 2,
        "integrity": {"expected_iterations": 1, "expected_result_count": 2},
        "system": {
            "minimum_available_ram_bytes": 2 * 1024 * 1024 * 1024,
            "peak_swap_growth_bytes": 128 * 1024 * 1024,
            "sustained_major_page_faults": False,
            "server_rss_sample_count": 10,
            "server_rss_error_count": 0,
            "maximum_server_rss_bytes": 8 * 1024 * 1024 * 1024,
            "nvidia_smi_enabled": True,
            "nvidia_smi_sample_count": 10,
            "nvidia_smi_error_count": 0,
            "maximum_vram_used_bytes": 4 * 1024 * 1024 * 1024,
            "maximum_gpu_temperature_celsius": 72.0,
        },
        "homebase": {"p95_degradation_percent": 12.0, "health_error_count": 0},
        "soak": {
            "duration_seconds": 1801.0,
            "completed_iterations": 1,
            "crash_count": 0,
            "oom_kill_count": 0,
        },
    }


def passing_input() -> tuple[dict, list[dict], dict, list[dict]]:
    candidate_run = run_metadata("candidate")
    candidate = [result("candidate", "en-case", "en"), result("candidate", "tr-case", "tr")]
    reference_run = run_metadata("reference")
    reference = [result("reference", "en-case", "en", 1.0), result("reference", "tr-case", "tr", 1.0)]
    return candidate_run, candidate, reference_run, reference


class AcceptanceTests(unittest.TestCase):
    def test_complete_candidate_and_real_reference_pass(self) -> None:
        run, results, reference_run, reference = passing_input()
        report = evaluate_acceptance(run, results, reference, reference_run)
        self.assertTrue(report["passed"])
        self.assertEqual("qwen38-acceptance/v2", report["schema_version"])
        self.assertEqual("external_results", report["reference_mode"])

    def test_embedded_fixture_score_is_provisional_and_cannot_pass(self) -> None:
        run, results, _, _ = passing_input()
        report = evaluate_acceptance(run, results)
        self.assertFalse(report["passed"])
        self.assertEqual("provisional_fixture_only", report["reference_mode"])
        self.assertEqual(0.9, report["provisional_fixture_quality_ratio"])

    def test_integrity_rejects_duplicates_errors_filters_and_hash_mismatch(self) -> None:
        run, results, reference_run, reference = passing_input()
        run["discovery"]["filtered"] = True
        results.append(deepcopy(results[0]))
        results[0]["error"] = "endpoint crashed"
        reference_run["fixtures"]["cases"]["sha256"] = "f" * 64
        report = evaluate_acceptance(run, results, reference, reference_run)
        failed = {gate["name"] for gate in report["gates"] if not gate["passed"]}
        self.assertIn("candidate_integrity_error_count", failed)
        self.assertIn("fixture_hashes_match", failed)
        self.assertIn("unfiltered_discovery", failed)

    def test_warmup_and_critical_safety_fail_closed(self) -> None:
        run, results, reference_run, reference = passing_input()
        run["warmup"] = {"success": False, "error": "timeout"}
        results[0]["safety"]["failure"] = True
        report = evaluate_acceptance(run, results, reference, reference_run)
        failed = {gate["name"] for gate in report["gates"] if not gate["passed"]}
        self.assertIn("warmup_succeeded", failed)
        self.assertIn("critical_safety_failures", failed)

    def test_missing_or_failed_rss_and_gpu_telemetry_rejects_run(self) -> None:
        run, results, reference_run, reference = passing_input()
        run["system"]["server_rss_sample_count"] = 0
        run["system"]["server_rss_error_count"] = 1
        run["system"]["maximum_server_rss_bytes"] = None
        run["system"]["nvidia_smi_enabled"] = False
        run["system"]["nvidia_smi_sample_count"] = 0
        run["system"]["nvidia_smi_error_count"] = 1
        run["system"]["maximum_vram_used_bytes"] = None
        run["system"]["maximum_gpu_temperature_celsius"] = None
        report = evaluate_acceptance(run, results, reference, reference_run)
        failed = {gate["name"] for gate in report["gates"] if not gate["passed"]}
        self.assertTrue(
            {
                "server_rss_sample_count",
                "server_rss_error_count",
                "maximum_server_rss_bytes",
                "nvidia_smi_enabled",
                "nvidia_smi_sample_count",
                "nvidia_smi_error_count",
                "maximum_vram_used_bytes",
                "maximum_gpu_temperature_celsius",
            }
            <= failed
        )

    def test_ttft_gate_uses_only_tagged_en_and_tr_cases(self) -> None:
        run, results, reference_run, reference = passing_input()
        extra = result("candidate", "quality-case", "en")
        extra["performance_gate"] = None
        extra["performance"]["ttft_seconds"] = 999.0
        reference_extra = deepcopy(extra)
        reference_extra["run_id"] = "reference"
        for metadata in (run, reference_run):
            metadata["discovery"]["expected_case_variants"].append(
                {
                    "case_id": "quality-case",
                    "variant": "base",
                    "language": "en",
                    "acceptance_eligible": True,
                    "performance_gate": None,
                }
            )
            metadata["result_count"] = 3
            metadata["integrity"]["expected_result_count"] = 3
        results.append(extra)
        reference.append(reference_extra)
        report = evaluate_acceptance(run, results, reference, reference_run)
        ttft_gate = next(gate for gate in report["gates"] if gate["name"] == "p95_ttft_seconds")
        self.assertTrue(ttft_gate["passed"])
        self.assertEqual(22.0, ttft_gate["measured"])


if __name__ == "__main__":
    unittest.main()
