"""Offline safety and contract tests for qwen_lab."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import qwen_lab


REVISION = "313447f257f7ebde0b968e4778feef774546ed81"


def locked_model(payload: bytes = b"fixture") -> dict[str, object]:
    return {
        "id": "target-q4",
        "repository": "owner/repository",
        "filename": "model.gguf",
        "revision": REVISION,
        "sha256": hashlib.sha256(payload).hexdigest(),
        "size_bytes": len(payload),
        "role": "target",
        "license": "Apache-2.0",
        "provenance": {"source": "official BF16", "requantized": False},
    }


def sanity_locked_model(payload: bytes = b"sanity") -> dict[str, object]:
    model = locked_model(payload)
    model["id"] = "sanity-model"
    model["role"] = qwen_lab.SANITY_ROLE
    return model


def write_lock(path: Path, model: dict[str, object]) -> None:
    path.write_text(
        json.dumps({"schema_version": 1, "models": [model]}),
        encoding="utf-8",
    )


class ModelLockTests(unittest.TestCase):
    def test_requires_sha_and_immutable_revision(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            lock = Path(temporary) / "models.lock.json"
            model = locked_model()
            model["sha256"] = None
            model["revision"] = "main"
            write_lock(lock, model)
            with self.assertRaises(qwen_lab.LabError):
                qwen_lab.load_models(lock)
            loaded = qwen_lab.load_models(lock, allow_unverified=True)
            self.assertIn("target-q4", loaded)

    def test_rejects_requantization_and_path_traversal(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            lock = Path(temporary) / "models.lock.json"
            model = locked_model()
            model["provenance"] = {"requantized": True}
            write_lock(lock, model)
            with self.assertRaisesRegex(qwen_lab.LabError, "requantized"):
                qwen_lab.load_models(lock)
            model = locked_model()
            model["filename"] = "../escape.gguf"
            write_lock(lock, model)
            with self.assertRaisesRegex(qwen_lab.LabError, "unsafe filename"):
                qwen_lab.load_models(lock)

    def test_rejects_storage_slug_collision(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            lock = Path(temporary) / "models.lock.json"
            first = locked_model()
            first["id"] = "owner/model"
            second = locked_model(b"second")
            second["id"] = "owner_model"
            lock.write_text(
                json.dumps({"schema_version": 1, "models": [first, second]}),
                encoding="utf-8",
            )
            with self.assertRaisesRegex(qwen_lab.LabError, "storage slug"):
                qwen_lab.load_models(lock)

    def test_only_locked_gguf_roles_are_executable(self) -> None:
        model = sanity_locked_model()
        qwen_lab.require_executable_model(model)
        model["role"] = "official_source_reference_not_for_4gb_execution"
        with self.assertRaisesRegex(qwen_lab.LabError, "non-executable"):
            qwen_lab.require_executable_model(model)
        model["role"] = qwen_lab.SANITY_ROLE
        model["filename"] = "index.json"
        with self.assertRaisesRegex(qwen_lab.LabError, "not a GGUF"):
            qwen_lab.require_executable_model(model)

    def test_huggingface_url_is_revision_pinned(self) -> None:
        url = qwen_lab.huggingface_url(locked_model())
        self.assertIn(f"/resolve/{REVISION}/model.gguf", url)
        self.assertNotIn("/resolve/main/", url)

    def test_dry_run_fetch_never_opens_network_or_creates_data_dir(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            lock = root / "models.lock.json"
            data = root / "must-not-exist"
            write_lock(lock, locked_model())
            with mock.patch("urllib.request.urlopen", side_effect=AssertionError("network used")):
                result = qwen_lab.main([
                    "--data-dir", str(data), "fetch", "target-q4",
                    "--lock", str(lock), "--dry-run",
                ])
            self.assertEqual(result, 0)
            self.assertFalse(data.exists())


class RuntimePlanTests(unittest.TestCase):
    def test_build_is_exactly_pinned(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            plan = qwen_lab.build_plan(Path(temporary), jobs=2)
        config = plan["config"]
        self.assertEqual(config["build"], "b10549")
        self.assertEqual(config["revision"], "b2e5e9b28b2484fbf94b543432ece638996a8b97")
        self.assertEqual(config["cuda"]["required_release"], "12.9")
        configure = plan["steps"][3]
        self.assertIn("-DCMAKE_CUDA_ARCHITECTURES=60;61", configure)
        self.assertIn("-DBUILD_SHARED_LIBS=ON", configure)
        self.assertIn("-DGGML_NATIVE=ON", configure)
        self.assertIn("-DLLAMA_BUILD_NUMBER=10549", configure)
        self.assertIn(
            "-DLLAMA_BUILD_COMMIT=b2e5e9b28b2484fbf94b543432ece638996a8b97",
            configure,
        )
        self.assertIn("llama-cli", config["targets"])
        self.assertIn("llama-perplexity", config["targets"])

    def test_server_profile_cannot_bind_externally(self) -> None:
        qwen_lab.require_loopback("127.0.0.1")
        qwen_lab.require_loopback("::1")
        with self.assertRaises(qwen_lab.LabError):
            qwen_lab.require_loopback("0.0.0.0")
        with self.assertRaises(qwen_lab.LabError):
            qwen_lab.require_loopback("localhost")

    def test_matrix_covers_required_axes(self) -> None:
        config = qwen_lab.load_json(qwen_lab.RUNTIME_CONFIG / "bench-matrix.json")
        cases = qwen_lab.matrix_cases(config, {"physical": 6, "logical": 12})
        self.assertTrue(any(case["mode"] == "cpu" for case in cases))
        self.assertTrue(any(case["mode"] == "cuda" for case in cases))
        self.assertEqual({case["context_depth"] for case in cases}, {2048, 4096})
        self.assertEqual({case["threads"] for case in cases}, {6, 12})
        self.assertEqual({case["ubatch_size"] for case in cases}, {64, 128})
        self.assertEqual({case["cache_type"] for case in cases}, {"q8_0", "q4_0"})
        cuda = [case for case in cases if case["mode"] == "cuda"]
        self.assertEqual({case["kv_location"] for case in cuda}, {"cpu", "gpu"})
        self.assertEqual({case["flash_attention"] for case in cases}, {"off", "on"})
        self.assertIn("auto", {case["gpu_layers"] for case in cuda})
        self.assertTrue({4, 8, 12, 16}.issubset({case["gpu_layers"] for case in cuda}))

    def test_matrix_auto_uses_fit_flags_and_offline_mode(self) -> None:
        config = qwen_lab.load_json(qwen_lab.RUNTIME_CONFIG / "bench-matrix.json")
        case = {
            "context_depth": 4096,
            "threads": 6,
            "ubatch_size": 64,
            "cache_type": "q8_0",
            "kv_location": "cpu",
            "flash_attention": "off",
            "gpu_layers": "auto",
        }
        argv = qwen_lab.bench_argv(Path("/tmp/lab"), locked_model(), config, case)
        self.assertIn("--offline", argv)
        self.assertEqual(argv[argv.index("--n-gpu-layers") + 1], "-1")
        self.assertEqual(argv[argv.index("--fit-target") + 1], "1024")
        self.assertEqual(argv[argv.index("--no-kv-offload") + 1], "1")

    def test_cpu_matrix_disables_every_gpu_offload_path(self) -> None:
        config = qwen_lab.load_runtime_config("bench-matrix.json")
        case = {
            "mode": "cpu",
            "context_depth": 2048,
            "threads": 6,
            "ubatch_size": 64,
            "cache_type": "q8_0",
            "kv_location": "cpu",
            "flash_attention": "off",
            "gpu_layers": 0,
        }
        argv = qwen_lab.bench_argv(Path("/tmp/lab"), locked_model(), config, case)
        self.assertEqual(argv[argv.index("--device") + 1], "none")
        self.assertEqual(argv[argv.index("--no-op-offload") + 1], "1")
        self.assertEqual(argv[argv.index("--n-gpu-layers") + 1], "0")

    def test_sanity_servers_are_loopback_uncached_and_separate_devices(self) -> None:
        config = qwen_lab.sanity_config()
        model = sanity_locked_model()
        cpu = qwen_lab.sanity_server_argv(Path("/tmp/lab"), model, config, "cpu", port=18089)
        cuda = qwen_lab.sanity_server_argv(
            Path("/tmp/lab"), model, config, "cuda", port=18090, cuda_device="CUDA7"
        )
        for argv in (cpu, cuda):
            self.assertTrue(argv[0].endswith("/llama-server"))
            self.assertIn("--offline", argv)
            self.assertIn("--no-mmproj", argv)
            self.assertEqual(argv[argv.index("--host") + 1], "127.0.0.1")
            self.assertEqual(argv[argv.index("--parallel") + 1], "1")
            self.assertIn("--no-cache-prompt", argv)
            self.assertEqual(argv[argv.index("--cache-ram") + 1], "0")
            self.assertEqual(argv[argv.index("--cache-reuse") + 1], "0")
            self.assertEqual(argv[argv.index("--flash-attn") + 1], "off")
        self.assertEqual(cpu[cpu.index("--port") + 1], "18089")
        self.assertEqual(cuda[cuda.index("--port") + 1], "18090")
        self.assertEqual(cpu[cpu.index("--device") + 1], "none")
        self.assertIn("--no-op-offload", cpu)
        self.assertIn("--no-kv-offload", cpu)
        self.assertEqual(cpu[cpu.index("--fit") + 1], "off")
        self.assertEqual(cuda[cuda.index("--device") + 1], "CUDA7")
        self.assertEqual(cuda[cuda.index("--n-gpu-layers") + 1], "auto")
        self.assertIn("--op-offload", cuda)
        self.assertIn("--kv-offload", cuda)
        self.assertEqual(cuda[cuda.index("--fit") + 1], "on")
        self.assertEqual(cuda[cuda.index("--fit-target") + 1], "1024")

    def test_sanity_completion_request_is_deterministic_and_returns_token_ids(self) -> None:
        request = qwen_lab.sanity_completion_request(qwen_lab.sanity_config())
        self.assertEqual(request["temperature"], 0)
        self.assertEqual(request["samplers"], ["temperature"])
        self.assertEqual(request["seed"], 424242)
        self.assertEqual(request["n_predict"], 32)
        self.assertFalse(request["cache_prompt"])
        self.assertTrue(request["return_tokens"])
        self.assertFalse(request["stream"])

    def test_sanity_server_leg_posts_once_and_terminates_cleanly(self) -> None:
        process = mock.Mock()
        process.poll.return_value = None
        process.wait.return_value = 0
        response = {"content": "DELTANET_OK_731", "tokens": [11, 22, 33]}
        with (
            mock.patch.object(qwen_lab, "reserve_loopback_port", return_value=19001),
            mock.patch.object(qwen_lab, "thread_counts", return_value={"physical": 6, "logical": 12}),
            mock.patch.object(qwen_lab.subprocess, "Popen", return_value=process),
            mock.patch.object(
                qwen_lab,
                "wait_for_sanity_server",
                return_value={"attempts": 2, "status_code": 200, "response": {"status": "ok"}},
            ),
            mock.patch.object(qwen_lab, "http_json", return_value=(200, response)) as post,
        ):
            result = qwen_lab.run_sanity_server_leg(
                Path("/tmp/lab"), sanity_locked_model(), qwen_lab.sanity_config(), "cpu"
            )
        self.assertTrue(result["passed"])
        self.assertEqual(result["tokens"], [11, 22, 33])
        self.assertEqual(result["shutdown"]["method"], "sigterm")
        process.terminate.assert_called_once_with()
        post.assert_called_once()
        self.assertEqual(post.call_args.args[0], "http://127.0.0.1:19001/completion")
        self.assertFalse(post.call_args.args[1]["cache_prompt"])

    def test_sanity_server_leg_cleans_up_after_readiness_failure(self) -> None:
        process = mock.Mock()
        process.poll.return_value = None
        process.wait.return_value = 0
        with (
            mock.patch.object(qwen_lab, "reserve_loopback_port", return_value=19002),
            mock.patch.object(qwen_lab, "thread_counts", return_value={"physical": 6, "logical": 12}),
            mock.patch.object(qwen_lab.subprocess, "Popen", return_value=process),
            mock.patch.object(
                qwen_lab,
                "wait_for_sanity_server",
                side_effect=qwen_lab.LabError("startup failed"),
            ),
            mock.patch.object(qwen_lab, "http_json") as post,
        ):
            result = qwen_lab.run_sanity_server_leg(
                Path("/tmp/lab"), sanity_locked_model(), qwen_lab.sanity_config(), "cpu"
            )
        self.assertFalse(result["passed"])
        self.assertIn("startup failed", result["request_error"])
        process.terminate.assert_called_once_with()
        post.assert_not_called()

    def test_generation_checks_fail_closed(self) -> None:
        config = qwen_lab.sanity_config()
        self.assertTrue(qwen_lab.generation_checks("DELTANET_OK_731", config)["passed"])
        self.assertFalse(qwen_lab.generation_checks("", config)["passed"])
        self.assertFalse(qwen_lab.generation_checks("DELTANET_OK_731 nan", config)["passed"])
        self.assertFalse(qwen_lab.generation_checks("DELTANET_OK_731 \ufffd", config)["passed"])

    def test_optimization_profiles_use_exact_pinned_flags(self) -> None:
        profiles = qwen_lab.optimization_profiles()
        self.assertGreaterEqual(profiles["minimum_speedup_percent"], 10)
        self.assertEqual(profiles["default_profile"], "baseline")
        self.assertEqual(profiles["profiles"]["baseline"]["server_args"][:3], ["--no-cache-prompt", "--cache-ram", "0"])
        self.assertIn("--spec-default", profiles["profiles"]["ngram"]["server_args"])
        self.assertEqual(profiles["profiles"]["q4-kv"]["cache_type_k"], "q4_0")
        self.assertEqual(
            profiles["profiles"]["prompt-cache"]["server_args"],
            ["--cache-prompt", "--cache-ram", "128", "--no-cache-idle-slots", "--cache-reuse", "64"],
        )

    def test_server_baseline_is_explicitly_offline_and_uncached(self) -> None:
        with mock.patch.object(qwen_lab, "thread_counts", return_value={"physical": 6, "logical": 12}):
            plan = qwen_lab.server_plan(Path("/tmp/lab"), locked_model(), optimization_profile="baseline")
        self.assertIn("--offline", plan["argv"])
        self.assertIn("--no-mmproj", plan["argv"])
        self.assertIn("--no-cache-prompt", plan["argv"])
        self.assertEqual(plan["minimum_speedup_percent"], 10)


class AttestationAndGateTests(unittest.TestCase):
    def test_project_libraries_must_resolve_below_build(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binary = root / "bin" / "llama-server"
            binary.parent.mkdir()
            binary.write_bytes(b"binary")
            binary.chmod(0o755)
            stale = "libllama.so.0 => /usr/local/lib/libllama.so.0 (0x1)\n"
            with mock.patch.object(
                qwen_lab,
                "run_capture",
                return_value={"returncode": 0, "stdout": stale},
            ):
                with self.assertRaisesRegex(qwen_lab.LabError, "stale or external"):
                    qwen_lab.validate_dynamic_binary(binary, root, require_project_libraries=True)

    def test_sanity_approval_absence_and_staleness_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            data = Path(temporary)
            models = {"sanity-model": sanity_locked_model()}
            attestation = {
                "revision": "a" * 40,
                "build": "b10549",
                "runtime_fingerprint": "f" * 64,
            }
            with self.assertRaisesRegex(qwen_lab.LabError, "configuration not found"):
                qwen_lab.require_sanity_approval(data, models, attestation)
            approval = {
                "schema_version": 1,
                "approved": True,
                "passed": True,
                "model_id": "sanity-model",
                "model_sha256": models["sanity-model"]["sha256"],
                "sanity_config_sha256": qwen_lab.sanity_config_sha256(),
                "runtime_revision": attestation["revision"],
                "runtime_build": attestation["build"],
                "runtime_fingerprint": "0" * 64,
            }
            qwen_lab.atomic_json(qwen_lab.sanity_approval_path(data), approval)
            with self.assertRaisesRegex(qwen_lab.LabError, "stale or invalid"):
                qwen_lab.require_sanity_approval(data, models, attestation)
            approval["runtime_fingerprint"] = attestation["runtime_fingerprint"]
            qwen_lab.atomic_json(qwen_lab.sanity_approval_path(data), approval)
            self.assertTrue(qwen_lab.require_sanity_approval(data, models, attestation)["approved"])

    def test_sanity_writes_approval_only_when_explicit(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            data = Path(temporary)
            models = {"sanity-model": sanity_locked_model()}
            leg = {
                "passed": True,
                "generation": {"text": "DELTANET_OK_731"},
                "tokens": [101, 202, 303],
            }
            attestation = {
                "revision": "a" * 40,
                "build": "b10549",
                "runtime_fingerprint": "f" * 64,
            }
            with (
                mock.patch.object(qwen_lab, "verify_model_file", return_value={"sha256": models["sanity-model"]["sha256"]}),
                mock.patch.object(qwen_lab, "attest_runtime", return_value=attestation),
                mock.patch.object(qwen_lab, "detect_cuda_device", return_value={"name": "CUDA0"}),
                mock.patch.object(qwen_lab, "run_sanity_server_leg", side_effect=[leg, leg, leg, leg]),
                mock.patch.object(qwen_lab, "sanity_config_sha256", return_value="c" * 64),
            ):
                report = qwen_lab.run_sanity(data, models, approve=False)
                self.assertTrue(report["passed"])
                self.assertFalse(qwen_lab.sanity_approval_path(data).exists())
                report = qwen_lab.run_sanity(data, models, approve=True)
                self.assertTrue(report["approved"])
                self.assertTrue(qwen_lab.sanity_approval_path(data).is_file())

    def test_bench_json_is_attested_and_normalized(self) -> None:
        revision = "b2e5e9b28b2484fbf94b543432ece638996a8b97"
        payload = [{
            "build_commit": "b2e5e9b2",
            "build_number": 10549,
            "n_prompt": 0,
            "n_gen": 128,
            "n_depth": 2048,
            "samples_ts": [0.9, 1.1, 1.0],
        }]
        metrics = qwen_lab.parse_bench_metrics(json.dumps(payload), revision, "b10549")
        self.assertEqual(metrics[0]["test"], "decode")
        self.assertEqual(metrics[0]["median_tokens_per_second"], 1.0)
        payload[0]["build_number"] = 1
        with self.assertRaisesRegex(qwen_lab.LabError, "build mismatch"):
            qwen_lab.parse_bench_metrics(json.dumps(payload), revision, "b10549")

    def test_expected_oom_is_classified(self) -> None:
        config = qwen_lab.load_runtime_config("bench-matrix.json")
        self.assertEqual(
            qwen_lab.classify_bench_failure(1, "", "CUDA error: out of memory", config),
            "expected_failure",
        )
        self.assertEqual(qwen_lab.classify_bench_failure(9, "", "killed", config), "unexpected_failure")

    def test_post_warmup_memory_thrashing_is_eliminated(self) -> None:
        config = qwen_lab.load_runtime_config("bench-matrix.json")["telemetry"]
        samples = []
        for index, elapsed in enumerate((31.0, 33.0, 35.0, 37.0)):
            samples.append({
                "elapsed_seconds": elapsed,
                "system": {
                    "memory_available_bytes": 2 * 1024**3,
                    "swap_used_bytes": index * 100 * 1024**2,
                    "major_faults": index * 20,
                    "swapin_pages": index * 1024,
                },
                "process": {
                    "VmRSS_bytes": 10 * 1024**3,
                    "VmSwap_bytes": index * 100 * 1024**2,
                    "major_faults": index * 20,
                },
                "gpu_process_memory_mib": 3500,
            })
        summary = qwen_lab.summarize_bench_telemetry(samples, config)
        self.assertEqual(summary["verdict"], "fail")
        self.assertEqual(summary["peak_process_gpu_memory_mib"], 3500)
        self.assertEqual(summary["checks"]["swap_growth"]["status"], "fail")
        self.assertEqual(summary["checks"]["sustained_major_faults"]["status"], "fail")


class ProbeTests(unittest.TestCase):
    def test_read_only_disk_probe(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "fixture.bin"
            payload = os.urandom(64 * 1024)
            path.write_bytes(payload)
            before = hashlib.sha256(path.read_bytes()).hexdigest()
            result = qwen_lab.disk_read_probe(path, len(payload), 4096)
            after = hashlib.sha256(path.read_bytes()).hexdigest()
            self.assertTrue(result["read_only"])
            self.assertEqual(result["bytes_read"], len(payload))
            self.assertGreater(result["bytes_per_second"], 0)
            self.assertEqual(before, after)

    def test_doctor_reports_pass_fail_and_unknown(self) -> None:
        snapshot = {
            "cpu": {"architecture": "x86_64"},
            "memory": {"MemTotal_bytes": 16 * 1024**3},
            "storage": {"target_usage_bytes": {"free": 100 * 1024**3}},
            "cuda": {"release": "12.9"},
            "nvidia": {"gpus": [{
                "compute_capability": "6.1",
                "memory.total": "4096",
                "driver_version": "580.82.07",
            }]},
        }
        self.assertEqual(qwen_lab.doctor_verdict(snapshot)["status"], "pass")
        snapshot["cuda"]["release"] = "13.0"
        self.assertEqual(qwen_lab.doctor_verdict(snapshot)["status"], "fail")
        snapshot["cuda"]["release"] = None
        snapshot["nvidia"]["gpus"] = []
        verdict = qwen_lab.doctor_verdict(snapshot)
        self.assertEqual(verdict["status"], "unknown")
        self.assertEqual(verdict["checks"]["pascal_compute_capability"]["status"], "unknown")


if __name__ == "__main__":
    unittest.main()
