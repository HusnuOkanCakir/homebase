from __future__ import annotations

import json
import sys
import tempfile
import unittest
from copy import deepcopy
from pathlib import Path


LAB_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(LAB_ROOT))

from tools.compare_optimization import compare_optimization, main  # noqa: E402


def make_run(run_id: str, runtime_profile: str) -> dict:
    expected = [
        {
            "case_id": case_id,
            "variant": "base",
            "language": language,
            "acceptance_eligible": True,
            "performance_gate": None,
        }
        for case_id, language in (("chat-en", "en"), ("chat-tr", "tr"))
    ]
    return {
        "schema_version": "qwen38-eval-run/v2",
        "run_id": run_id,
        "profile": "official",
        "identity": {
            "model_sha256": "a" * 64,
            "runtime_revision": "b" * 40,
            "runtime_profile": runtime_profile,
        },
        "fixtures": {
            name: {"sha256": character * 64}
            for name, character in (("cases", "c"), ("corpus", "d"), ("profiles", "e"))
        },
        "discovery": {"filtered": False, "expected_case_variants": expected},
        "result_count": 2,
        "integrity": {"expected_iterations": 1, "expected_result_count": 2},
        "soak": {"completed_iterations": 1},
    }


def make_results(run_id: str, total_seconds: float, score: float = 1.0) -> list[dict]:
    return [
        {
            "schema_version": "qwen38-eval-result/v2",
            "run_id": run_id,
            "profile": "official",
            "iteration": 1,
            "case_id": case_id,
            "variant": "base",
            "language": language,
            "acceptance_eligible": True,
            "performance_gate": None,
            "error": None,
            "performance": {"completion_tokens": 100, "total_seconds": total_seconds},
            "quality": {"score": score},
        }
        for case_id, language in (("chat-en", "en"), ("chat-tr", "tr"))
    ]


class OptimizationComparisonTests(unittest.TestCase):
    def setUp(self) -> None:
        self.baseline_run = make_run("baseline-run", "baseline")
        self.baseline = make_results("baseline-run", 10.0)
        self.candidate_run = make_run("candidate-run", "q4-kv")
        self.candidate = make_results("candidate-run", 8.0)

    def test_retains_optimization_above_ten_percent_without_quality_regression(self) -> None:
        report = compare_optimization(
            self.baseline_run,
            self.baseline,
            self.candidate_run,
            self.candidate,
        )
        self.assertTrue(report["passed"])
        self.assertAlmostEqual(25.0, report["throughput"]["improvement_percent"])

    def test_rejects_low_gain_and_any_paired_quality_regression(self) -> None:
        candidate = make_results("candidate-run", 9.5)
        candidate[0]["quality"]["score"] = 0.99
        report = compare_optimization(
            self.baseline_run,
            self.baseline,
            self.candidate_run,
            candidate,
        )
        failed = {gate["name"] for gate in report["gates"] if not gate["passed"]}
        self.assertIn("throughput_improvement_percent", failed)
        self.assertIn("quality_regression_count", failed)

    def test_rejects_identity_filter_and_result_integrity_mismatch(self) -> None:
        candidate_run = deepcopy(self.candidate_run)
        candidate_run["identity"]["model_sha256"] = "f" * 64
        candidate_run["discovery"]["filtered"] = True
        candidate = deepcopy(self.candidate)
        candidate[0]["error"] = "crash"
        report = compare_optimization(self.baseline_run, self.baseline, candidate_run, candidate)
        failed = {gate["name"] for gate in report["gates"] if not gate["passed"]}
        self.assertIn("model_sha256_matches", failed)
        self.assertIn("candidate_unfiltered", failed)
        self.assertIn("candidate_integrity_error_count", failed)

    def test_cli_writes_report_atomically(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            baseline_run = root / "baseline-run.json"
            baseline_results = root / "baseline-results.jsonl"
            candidate_run = root / "candidate-run.json"
            candidate_results = root / "candidate-results.jsonl"
            output = root / "comparison.json"
            baseline_run.write_text(json.dumps(self.baseline_run), encoding="utf-8")
            candidate_run.write_text(json.dumps(self.candidate_run), encoding="utf-8")
            baseline_results.write_text(
                "".join(json.dumps(item) + "\n" for item in self.baseline), encoding="utf-8"
            )
            candidate_results.write_text(
                "".join(json.dumps(item) + "\n" for item in self.candidate), encoding="utf-8"
            )
            exit_code = main(
                [
                    "--baseline-run",
                    str(baseline_run),
                    "--baseline-results",
                    str(baseline_results),
                    "--candidate-run",
                    str(candidate_run),
                    "--candidate-results",
                    str(candidate_results),
                    "--output",
                    str(output),
                ]
            )
            self.assertEqual(0, exit_code)
            self.assertTrue(json.loads(output.read_text(encoding="utf-8"))["passed"])
            self.assertEqual([], list(root.glob(".*.tmp")))


if __name__ == "__main__":
    unittest.main()
