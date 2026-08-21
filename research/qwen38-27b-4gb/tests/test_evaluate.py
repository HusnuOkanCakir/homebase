from __future__ import annotations

import json
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock


LAB_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(LAB_ROOT))

from tools.evaluate import (  # noqa: E402
    DEFAULT_CORPUS,
    HomebaseProbe,
    build_parser,
    expand_long_context,
    load_jsonl,
    load_profile,
    load_sandbox_attestation,
    run_evaluation,
    validate_loopback_url,
)


MODEL_SHA = "a" * 64
RUNTIME_REVISION = "b" * 40


class MockHTTPResponse:
    def __init__(self, body: bytes) -> None:
        self.lines = body.splitlines(keepends=True)

    def __enter__(self) -> "MockHTTPResponse":
        return self

    def __exit__(self, *args: object) -> None:
        pass

    def __iter__(self):
        return iter(self.lines)


class MockOpenAIServer:
    def __init__(self) -> None:
        self.requests: list[dict] = []
        self.delay_seconds = 0.0

    def urlopen(self, request, timeout: float):
        if self.delay_seconds:
            time.sleep(self.delay_seconds)
        self.requests.append(json.loads(request.data))
        events = [
            {"choices": [{"delta": {"content": "Tuesday 22:40 UTC, LARCH-17 "}}]},
            {
                "choices": [
                    {
                        "delta": {"content": "[source:atlas-en#atlas-en-0000]"},
                        "finish_reason": "stop",
                    }
                ]
            },
            {"choices": [], "usage": {"completion_tokens": 9}},
        ]
        body = "".join(f"data: {json.dumps(event)}\n\n" for event in events) + "data: [DONE]\n\n"
        return MockHTTPResponse(body.encode())


class EvaluationTests(unittest.TestCase):
    def setUp(self) -> None:
        self.server = MockOpenAIServer()

    def test_loopback_harness_records_rag_citations_and_sends_no_tools(self) -> None:
        case = {
            "fixture_version": "qwen38-eval-case/v1",
            "case_id": "mock-rag",
            "language": "en",
            "category": "rag_grounded_qa",
            "prompt": "When is Atlas maintenance and what is the code?",
            "rag_query": "Atlas maintenance recovery code",
            "variants": ["rag"],
            "acceptance_variants": ["rag"],
            "checks": [{"type": "contains_all", "values": ["Tuesday", "22:40", "LARCH-17"]}],
            "rag_checks": [{"type": "citation_document", "value": "atlas-en"}],
            "reference_score": 1.0,
        }
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            cases = root / "cases.jsonl"
            cases.write_text(json.dumps(case) + "\n", encoding="utf-8")
            attestation = root / "sandbox.json"
            attestation.write_text(
                json.dumps(
                    {
                        "schema_version": "qwen38-sandbox-attestation/v2",
                        "attester": "unit-test",
                        "issued_at": "2026-08-21T12:00:00Z",
                        "runtime_pid": 4242,
                        "runtime_start_ticks": 12345,
                        "network_namespace_inode": 111,
                        "mount_namespace_inode": 222,
                        "rootfs_read_only": True,
                        "external_network_access": False,
                        "api_loopback_only": True,
                        "tools_enabled": False,
                        "host_mounts": [],
                        "model_sha256": MODEL_SHA,
                        "runtime_revision": RUNTIME_REVISION,
                    }
                ),
                encoding="utf-8",
            )
            output = root / "caller-output"
            self.server.delay_seconds = 0.002
            args = build_parser().parse_args(
                [
                    "--endpoint",
                    "http://127.0.0.1:18080/v1",
                    "--model",
                    "mock",
                    "--model-sha256",
                    MODEL_SHA,
                    "--runtime-revision",
                    RUNTIME_REVISION,
                    "--profile",
                    "uncensored",
                    "--sandbox-attestation",
                    str(attestation),
                    "--cases",
                    str(cases),
                    "--corpus",
                    str(DEFAULT_CORPUS),
                    "--output-dir",
                    str(output),
                    "--sample-interval",
                    "0.01",
                    "--soak-seconds",
                    "0.003",
                ]
            )
            live = {
                "runtime_pid": 4242,
                "runtime_start_ticks": 12345,
                "network_namespace_inode": 111,
                "mount_namespace_inode": 222,
                "rootfs_read_only": True,
            }
            with mock.patch("tools.evaluate.NO_PROXY_OPENER.open", side_effect=self.server.urlopen), mock.patch(
                "tools.evaluate.read_live_runtime_evidence", return_value=live
            ):
                run, results = run_evaluation(args)
            on_disk = load_jsonl(output / "results.jsonl")
        self.assertEqual("qwen38-eval-run/v2", run["schema_version"])
        self.assertEqual("baseline", run["identity"]["runtime_profile"])
        self.assertEqual(results, on_disk)
        self.assertEqual(1.0, results[0]["quality"]["score"])
        self.assertEqual("atlas-en", results[0]["response"]["citations"][0]["document_id"])
        self.assertTrue(run["warmup"]["success"])
        self.assertGreaterEqual(run["soak"]["completed_iterations"], 2)
        self.assertEqual(run["soak"]["completed_iterations"], len(results))
        self.assertTrue(all(not request.get("tools") for request in self.server.requests))
        self.assertIn("read-only sandbox", self.server.requests[-1]["messages"][0]["content"])
        self.assertFalse(run["sandbox_attestation"]["document"]["external_network_access"])
        self.assertGreater(results[0]["performance"]["decode_tokens_per_second"], 0)

    def test_non_loopback_endpoint_is_rejected(self) -> None:
        with self.assertRaises(ValueError):
            validate_loopback_url("https://example.com/v1")
        with self.assertRaises(ValueError):
            validate_loopback_url("http://localhost:8000/v1")

    def test_profiles_and_fixture_coverage(self) -> None:
        profile = load_profile(LAB_ROOT / "eval" / "profiles.json", "uncensored")
        self.assertEqual("sandbox-only", profile["deployment"])
        self.assertTrue(profile["read_only"])
        self.assertFalse(profile["tools_enabled"])
        cases = load_jsonl(LAB_ROOT / "eval" / "fixtures" / "cases.jsonl")
        categories = {case["category"] for case in cases}
        self.assertTrue(
            {
                "chat",
                "summarization",
                "reasoning",
                "instruction_following",
                "factual_qa",
                "long_context",
                "rag_grounded_qa",
                "prompt_injection",
                "performance_ttft",
            }
            <= categories
        )
        self.assertEqual({"en", "tr"}, {case["language"] for case in cases})
        long_cases = {case["case_id"]: case for case in cases if case["category"] == "long_context"}
        self.assertEqual(2048, expand_long_context(long_cases["long-2k-en"])[1])
        self.assertEqual(2048, expand_long_context(long_cases["long-2k-tr"])[1])
        self.assertEqual(4096, expand_long_context(long_cases["long-4k-en"])[1])
        self.assertEqual(4096, expand_long_context(long_cases["long-4k-tr"])[1])
        ttft_cases = [case for case in cases if case.get("performance_gate") == "ttft"]
        self.assertEqual({"en", "tr"}, {case["language"] for case in ttft_cases})
        self.assertTrue(all(expand_long_context(case)[1] == 512 for case in ttft_cases))

    def test_uncensored_attestation_is_mandatory_and_strict(self) -> None:
        with self.assertRaisesRegex(ValueError, "requires --sandbox-attestation"):
            load_sandbox_attestation(
                None,
                "uncensored",
                MODEL_SHA,
                RUNTIME_REVISION,
                "http://127.0.0.1:8000/v1",
            )
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "bad.json"
            path.write_text(
                json.dumps(
                    {
                        "schema_version": "qwen38-sandbox-attestation/v2",
                        "attester": "unit-test",
                        "issued_at": "2026-08-21T12:00:00Z",
                        "runtime_pid": 4242,
                        "runtime_start_ticks": 12345,
                        "network_namespace_inode": 111,
                        "mount_namespace_inode": 222,
                        "rootfs_read_only": True,
                        "external_network_access": True,
                        "api_loopback_only": True,
                        "tools_enabled": False,
                        "host_mounts": [],
                        "model_sha256": MODEL_SHA,
                        "runtime_revision": RUNTIME_REVISION,
                    }
                ),
                encoding="utf-8",
            )
            with self.assertRaisesRegex(ValueError, "external_network_access"):
                load_sandbox_attestation(
                    path,
                    "uncensored",
                    MODEL_SHA,
                    RUNTIME_REVISION,
                    "http://127.0.0.1:8000/v1",
                )

    def test_timeout_and_sample_interval_must_be_positive(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = [
                "--output-dir",
                temporary,
                "--model-sha256",
                MODEL_SHA,
                "--runtime-revision",
                RUNTIME_REVISION,
            ]
            timeout_args = build_parser().parse_args(base + ["--timeout", "0"])
            interval_args = build_parser().parse_args(base + ["--sample-interval", "-1"])
            with self.assertRaisesRegex(ValueError, "timeout"):
                run_evaluation(timeout_args)
            with self.assertRaisesRegex(ValueError, "sample interval"):
                run_evaluation(interval_args)

    def test_homebase_probe_counts_attempts_and_errors(self) -> None:
        probe = HomebaseProbe("http://127.0.0.1:8080/health", interval=0.01)
        with mock.patch("tools.evaluate._probe", side_effect=OSError("offline")):
            probe.start()
            report = probe.finish()
        self.assertGreaterEqual(report["baseline_attempt_count"], 5)
        self.assertEqual(
            report["health_error_count"],
            report["baseline_error_count"] + report["during_error_count"],
        )


if __name__ == "__main__":
    unittest.main()
