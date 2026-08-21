#!/usr/bin/env python3
"""Dependency-free runtime tooling for the isolated Qwen 3.8 27B lab."""

from __future__ import annotations

import argparse
import datetime as dt
import difflib
import hashlib
import ipaddress
import itertools
import json
import os
from pathlib import Path
import platform
import re
import shlex
import shutil
import socket
import statistics
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Iterable, Mapping, Sequence


LAB_ROOT = Path(__file__).resolve().parent.parent
RUNTIME_CONFIG = LAB_ROOT / "config" / "runtime"
DEFAULT_MODEL_LOCK = LAB_ROOT / "config" / "models.lock.json"
DEFAULT_DATA_DIR = Path("/srv/qwen-lab")
SHA256_RE = re.compile(r"^[0-9a-fA-F]{64}$")
REVISION_RE = re.compile(r"^[0-9a-fA-F]{40,64}$")
SANITY_ROLE = "gated_deltanet_cpu_cuda_sanity"
SANITY_APPROVAL = Path("approvals/gated-deltanet-cpu-cuda-v1.json")
EXECUTABLE_ROLES = {
    SANITY_ROLE,
    "initial_q2_cpu_partial_gpu_candidate",
    "initial_q3_cpu_partial_gpu_candidate",
    "q4_reference_colab_only_when_vram_at_least_15gb",
}
DOWNLOAD_RESERVE_BYTES = 2 * 1024**3
RUN_RESERVE_BYTES = 1024**3
ANSI_RE = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]")
BAD_FLOAT_RE = re.compile(r"(?<![A-Za-z])(?:nan|[-+]?inf(?:inity)?)(?![A-Za-z])", re.IGNORECASE)
NO_PROXY_OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


class LabError(RuntimeError):
    """An expected, operator-actionable failure."""


def load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise LabError(f"configuration not found: {path}") from exc
    except (OSError, json.JSONDecodeError) as exc:
        raise LabError(f"cannot read JSON configuration {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise LabError(f"configuration root must be an object: {path}")
    return value


def require_schema_one(config: Mapping[str, Any], path: Path) -> None:
    if config.get("schema_version") != 1:
        raise LabError(f"{path}: unsupported schema_version (expected 1)")


def run_capture(argv: Sequence[str], timeout: int = 15) -> dict[str, Any]:
    executable = shutil.which(argv[0])
    if executable is None:
        return {"available": False, "argv": list(argv), "error": "command not found"}
    try:
        result = subprocess.run(
            [executable, *argv[1:]],
            check=False,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        return {"available": True, "argv": list(argv), "error": str(exc)}
    record: dict[str, Any] = {
        "available": True,
        "argv": list(argv),
        "returncode": result.returncode,
    }
    if result.stdout.strip():
        record["stdout"] = result.stdout.strip()
    if result.stderr.strip():
        record["stderr"] = result.stderr.strip()
    return record


def run_checked(argv: Sequence[str], cwd: Path | None = None) -> subprocess.CompletedProcess[str]:
    try:
        return subprocess.run(list(argv), cwd=cwd, check=True, text=True)
    except FileNotFoundError as exc:
        raise LabError(f"required command not found: {argv[0]}") from exc
    except subprocess.CalledProcessError as exc:
        raise LabError(f"command failed ({exc.returncode}): {shlex.join(argv)}") from exc


def read_text(path: Path) -> str | None:
    try:
        return path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return None


def parse_meminfo() -> dict[str, int]:
    result: dict[str, int] = {}
    text = read_text(Path("/proc/meminfo")) or ""
    for line in text.splitlines():
        match = re.match(r"^([^:]+):\s+(\d+)\s*(kB)?$", line)
        if match:
            multiplier = 1024 if match.group(3) else 1
            result[f"{match.group(1)}_bytes"] = int(match.group(2)) * multiplier
    return result


def cpu_snapshot() -> dict[str, Any]:
    blocks = (read_text(Path("/proc/cpuinfo")) or "").strip().split("\n\n")
    processors: list[dict[str, str]] = []
    for block in blocks:
        item: dict[str, str] = {}
        for line in block.splitlines():
            if ":" in line:
                key, value = line.split(":", 1)
                item[key.strip()] = value.strip()
        if item:
            processors.append(item)
    first = processors[0] if processors else {}
    core_pairs = {
        (item.get("physical id", "0"), item["core id"])
        for item in processors
        if "core id" in item
    }
    sockets = {item["physical id"] for item in processors if "physical id" in item}
    logical = os.cpu_count() or len(processors) or 1
    physical = len(core_pairs) or logical
    flags = first.get("flags", first.get("Features", "")).split()
    return {
        "architecture": platform.machine(),
        "model": first.get("model name", first.get("Processor")),
        "logical_cores": logical,
        "physical_cores": physical,
        "sockets": len(sockets) or 1,
        "flags": sorted(set(flags)),
        "lscpu": run_capture(["lscpu", "--json"]),
    }


def json_command(argv: Sequence[str]) -> Any:
    record = run_capture(argv)
    if record.get("returncode") == 0 and "stdout" in record:
        try:
            return json.loads(record["stdout"])
        except json.JSONDecodeError:
            pass
    return record


def nvidia_snapshot() -> dict[str, Any]:
    pci = run_capture(["lspci", "-nn"])
    nvidia_pci = [
        line for line in str(pci.get("stdout", "")).splitlines() if "NVIDIA" in line
    ]
    fields = (
        "index,name,pci.bus_id,driver_version,memory.total,memory.free,"
        "memory.used,temperature.gpu,pstate"
    )
    query = run_capture(
        ["nvidia-smi", f"--query-gpu={fields}", "--format=csv,noheader,nounits"]
    )
    gpus: list[dict[str, Any]] = []
    if query.get("returncode") == 0:
        names = fields.split(",")
        for line in str(query.get("stdout", "")).splitlines():
            values = [value.strip() for value in line.split(",")]
            gpus.append(dict(zip(names, values)))
        compute = run_capture(
            ["nvidia-smi", "--query-gpu=index,compute_cap", "--format=csv,noheader,nounits"]
        )
        if compute.get("returncode") == 0:
            by_index = {str(gpu.get("index")): gpu for gpu in gpus}
            for line in str(compute.get("stdout", "")).splitlines():
                values = [value.strip() for value in line.split(",", 1)]
                if len(values) == 2 and values[0] in by_index:
                    by_index[values[0]]["compute_capability"] = values[1]
    return {
        "pci_devices": nvidia_pci,
        "nvidia_smi": query,
        "gpus": gpus,
        "driver_proc_version": read_text(Path("/proc/driver/nvidia/version")),
    }


def cuda_snapshot() -> dict[str, Any]:
    nvcc = run_capture(["nvcc", "--version"])
    combined = f"{nvcc.get('stdout', '')}\n{nvcc.get('stderr', '')}"
    match = re.search(r"release\s+(\d+\.\d+)", combined)
    return {
        "nvcc": nvcc,
        "release": match.group(1) if match else None,
        "cuda_home": os.environ.get("CUDA_HOME") or os.environ.get("CUDA_PATH"),
        "ldconfig_cuda": run_capture(["ldconfig", "-p"]),
    }


def sensors_snapshot() -> dict[str, Any]:
    sensors = json_command(["sensors", "-j"])
    thermal: list[dict[str, Any]] = []
    for zone in sorted(Path("/sys/class/thermal").glob("thermal_zone*")):
        temp = read_text(zone / "temp")
        kind = read_text(zone / "type")
        if temp is not None:
            try:
                value: float | str = int(temp.strip()) / 1000.0
            except ValueError:
                value = temp.strip()
            thermal.append({"zone": zone.name, "type": (kind or "").strip(), "celsius": value})
    return {"lm_sensors": sensors, "thermal_zones": thermal}


def filesystem_snapshot(data_dir: Path) -> dict[str, Any]:
    probe_path = existing_ancestor(data_dir)
    usage = shutil.disk_usage(probe_path)
    return {
        "target_data_dir": str(data_dir),
        "target_existing_ancestor": str(probe_path),
        "target_usage_bytes": {
            "total": usage.total,
            "used": usage.used,
            "free": usage.free,
        },
        "block_devices": json_command(
            [
                "lsblk", "--json", "--bytes", "--output",
                "NAME,KNAME,PATH,TYPE,SIZE,ROTA,TRAN,FSTYPE,FSVER,MOUNTPOINTS,MODEL",
            ]
        ),
        "filesystems": json_command(
            ["findmnt", "--json", "--bytes", "--output", "TARGET,SOURCE,FSTYPE,OPTIONS,SIZE,USED,AVAIL"]
        ),
    }


def hardware_snapshot(data_dir: Path) -> dict[str, Any]:
    return {
        "schema_version": 1,
        "captured_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "hostname": socket.gethostname(),
        "platform": platform.platform(),
        "kernel": platform.release(),
        "cpu": cpu_snapshot(),
        "memory": parse_meminfo(),
        "storage": filesystem_snapshot(data_dir),
        "nvidia": nvidia_snapshot(),
        "cuda": cuda_snapshot(),
        "sensors": sensors_snapshot(),
    }


def disk_read_probe(path: Path, byte_limit: int, block_bytes: int) -> dict[str, Any]:
    resolved = path.expanduser().resolve()
    if not resolved.is_file():
        raise LabError(f"disk throughput probe requires an existing regular file: {resolved}")
    if byte_limit <= 0 or block_bytes <= 0:
        raise LabError("disk throughput byte and block limits must be positive")
    length = min(resolved.stat().st_size, byte_limit)
    if length == 0:
        raise LabError(f"disk throughput probe file is empty: {resolved}")
    advice: dict[str, str] = {"drop_before": "unsupported", "sequential": "unsupported", "drop_after": "unsupported"}
    read_bytes = 0
    started = time.monotonic()
    with resolved.open("rb", buffering=0) as stream:
        if hasattr(os, "posix_fadvise"):
            try:
                os.posix_fadvise(stream.fileno(), 0, length, os.POSIX_FADV_DONTNEED)
                advice["drop_before"] = "requested"
                os.posix_fadvise(stream.fileno(), 0, length, os.POSIX_FADV_SEQUENTIAL)
                advice["sequential"] = "requested"
            except OSError as exc:
                advice["drop_before"] = f"failed: {exc}"
        started = time.monotonic()
        while read_bytes < length:
            chunk = stream.read(min(block_bytes, length - read_bytes))
            if not chunk:
                break
            read_bytes += len(chunk)
        elapsed = time.monotonic() - started
        if hasattr(os, "posix_fadvise"):
            try:
                os.posix_fadvise(stream.fileno(), 0, read_bytes, os.POSIX_FADV_DONTNEED)
                advice["drop_after"] = "requested"
            except OSError as exc:
                advice["drop_after"] = f"failed: {exc}"
    if read_bytes == 0 or elapsed <= 0:
        raise LabError(f"disk throughput probe could not read data from {resolved}")
    return {
        "path": str(resolved),
        "read_only": True,
        "bytes_read": read_bytes,
        "elapsed_seconds": elapsed,
        "bytes_per_second": read_bytes / elapsed,
        "mib_per_second": read_bytes / elapsed / 1024**2,
        "cache_advice": advice,
        "caveat": "best-effort sequential file read; cache eviction is advisory and this is not a destructive disk test",
    }


def verdict_item(expected: Any, actual: Any, passes: bool | None) -> dict[str, Any]:
    status = "unknown" if passes is None else ("pass" if passes else "fail")
    return {"status": status, "expected": expected, "actual": actual}


def doctor_verdict(snapshot: Mapping[str, Any]) -> dict[str, Any]:
    requirements = load_runtime_config("requirements.json")
    checks: dict[str, dict[str, Any]] = {}

    actual_arch = snapshot.get("cpu", {}).get("architecture")
    checks["architecture"] = verdict_item(
        requirements["architecture"], actual_arch,
        None if not actual_arch else actual_arch in {requirements["architecture"], "amd64"},
    )
    mem_total = snapshot.get("memory", {}).get("MemTotal_bytes")
    checks["ram"] = verdict_item(
        {"class": requirements["ram_class"], "minimum_bytes": requirements["ram_min_bytes"]}, mem_total,
        None if mem_total is None else int(mem_total) >= int(requirements["ram_min_bytes"]),
    )
    free = snapshot.get("storage", {}).get("target_usage_bytes", {}).get("free")
    checks["data_free_space"] = verdict_item(
        {"minimum_bytes": requirements["data_free_min_bytes"]}, free,
        None if free is None else int(free) >= int(requirements["data_free_min_bytes"]),
    )
    cuda_release = snapshot.get("cuda", {}).get("release")
    checks["cuda_toolkit"] = verdict_item(
        requirements["cuda_release"], cuda_release,
        None if not cuda_release else str(cuda_release) == str(requirements["cuda_release"]),
    )

    gpus = snapshot.get("nvidia", {}).get("gpus", [])
    gpu = gpus[0] if isinstance(gpus, list) and gpus else None
    capability = gpu.get("compute_capability") if isinstance(gpu, Mapping) else None
    checks["pascal_compute_capability"] = verdict_item(
        requirements["compute_capabilities"], capability,
        None if not capability else str(capability) in requirements["compute_capabilities"],
    )
    memory_mib: int | None = None
    if isinstance(gpu, Mapping):
        try:
            memory_mib = int(float(str(gpu.get("memory.total", ""))))
        except ValueError:
            memory_mib = None
    checks["gpu_memory"] = verdict_item(
        {
            "minimum_mib": requirements["gpu_memory_min_mib"],
            "maximum_mib": requirements["gpu_memory_max_mib"],
        },
        memory_mib,
        None if memory_mib is None else (
            int(requirements["gpu_memory_min_mib"]) <= memory_mib <= int(requirements["gpu_memory_max_mib"])
        ),
    )
    driver_version = gpu.get("driver_version") if isinstance(gpu, Mapping) else None
    driver_major: int | None = None
    if driver_version:
        match = re.match(r"^(\d+)", str(driver_version))
        if match:
            driver_major = int(match.group(1))
    checks["nvidia_driver_branch"] = verdict_item(
        {"major": requirements["driver_major"]}, driver_version,
        None if driver_major is None else driver_major == int(requirements["driver_major"]),
    )

    statuses = [item["status"] for item in checks.values()]
    overall = "fail" if "fail" in statuses else ("unknown" if "unknown" in statuses else "pass")
    return {"schema_version": 1, "status": overall, "checks": checks}


def data_dir_from_args(args: argparse.Namespace) -> Path:
    value = args.data_dir or os.environ.get("QWEN_LAB_DATA_DIR") or str(DEFAULT_DATA_DIR)
    return Path(value).expanduser().resolve()


def model_lock_path(args: argparse.Namespace) -> Path:
    return Path(getattr(args, "lock", None) or DEFAULT_MODEL_LOCK).expanduser().resolve()


def is_verified_model(model: Mapping[str, Any]) -> bool:
    return bool(SHA256_RE.fullmatch(str(model.get("sha256") or ""))) and bool(
        REVISION_RE.fullmatch(str(model.get("revision") or ""))
    )


def model_is_requantized(model: Mapping[str, Any]) -> bool:
    if str(model.get("role", "")).lower() in {"requantized", "requant"}:
        return True
    provenance = model.get("provenance")
    if isinstance(provenance, Mapping):
        if provenance.get("requantized") is True or provenance.get("source_is_quantized") is True:
            return True
        derivation = str(provenance.get("derivation", "")).lower()
        return derivation in {"requantization", "requantized"}
    if isinstance(provenance, str):
        return bool(re.search(r"\brequantized\s+from\b", provenance, re.IGNORECASE))
    return False


def load_models(path: Path, allow_unverified: bool = False) -> dict[str, dict[str, Any]]:
    config = load_json(path)
    require_schema_one(config, path)
    raw_models = config.get("models")
    if not isinstance(raw_models, list) or not raw_models:
        raise LabError(f"{path}: models must be a non-empty array")
    result: dict[str, dict[str, Any]] = {}
    slugs: dict[str, str] = {}
    for index, raw in enumerate(raw_models):
        if not isinstance(raw, dict):
            raise LabError(f"{path}: models[{index}] must be an object")
        model = dict(raw)
        missing = [
            key for key in ("id", "repository", "filename", "revision", "role", "license", "provenance")
            if model.get(key) in (None, "")
        ]
        if missing:
            raise LabError(f"{path}: models[{index}] missing {', '.join(missing)}")
        model_id = str(model["id"])
        if model_id in result:
            raise LabError(f"{path}: duplicate model id: {model_id}")
        slug = model_slug(model_id)
        if slug in slugs:
            raise LabError(
                f"{path}: model ids {slugs[slug]!r} and {model_id!r} share unsafe storage slug {slug!r}"
            )
        slugs[slug] = model_id
        filename = Path(str(model["filename"]))
        if filename.is_absolute() or ".." in filename.parts:
            raise LabError(f"{path}: unsafe filename for {model_id}: {filename}")
        size = model.get("size_bytes")
        if size is not None and (not isinstance(size, int) or isinstance(size, bool) or size <= 0):
            raise LabError(f"{path}: invalid size_bytes for {model_id}")
        if model_is_requantized(model):
            raise LabError(f"{path}: requantized artifacts are forbidden: {model_id}")
        if not is_verified_model(model) and not allow_unverified:
            raise LabError(
                f"{path}: {model_id} lacks immutable revision and SHA-256; "
                "resolve the lock or explicitly pass --allow-unverified"
            )
        result[model_id] = model
    return result


def require_executable_model(model: Mapping[str, Any]) -> None:
    role = str(model.get("role", ""))
    if role not in EXECUTABLE_ROLES:
        raise LabError(f"model {model.get('id')!r} has non-executable role {role!r}")
    if Path(str(model.get("filename", ""))).suffix.lower() != ".gguf":
        raise LabError(f"model {model.get('id')!r} is not a GGUF executable artifact")


def models_with_role(models: Mapping[str, dict[str, Any]], role: str) -> list[dict[str, Any]]:
    return [model for model in models.values() if model.get("role") == role]


def load_runtime_config(filename: str) -> dict[str, Any]:
    path = RUNTIME_CONFIG / filename
    config = load_json(path)
    require_schema_one(config, path)
    return config


def existing_ancestor(path: Path) -> Path:
    candidate = path
    while not candidate.exists() and candidate != candidate.parent:
        candidate = candidate.parent
    return candidate


def free_bytes(path: Path) -> int:
    return shutil.disk_usage(existing_ancestor(path)).free


def require_free_space(path: Path, payload_bytes: int, reserve_bytes: int) -> dict[str, int]:
    available = free_bytes(path)
    required = max(0, payload_bytes) + max(0, reserve_bytes)
    if available < required:
        raise LabError(
            f"insufficient free space below {existing_ancestor(path)}: "
            f"need {required} bytes ({payload_bytes} payload + {reserve_bytes} reserve), "
            f"have {available}"
        )
    return {"available_bytes": available, "required_bytes": required, "reserve_bytes": reserve_bytes}


def model_slug(model_id: str) -> str:
    slug = re.sub(r"[^A-Za-z0-9._-]+", "_", model_id).strip("._")
    if not slug:
        raise LabError(f"invalid model id: {model_id!r}")
    return slug


def model_path(data_dir: Path, model: Mapping[str, Any]) -> Path:
    return data_dir / "models" / model_slug(str(model["id"])) / str(model["filename"])


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(8 * 1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def verify_model_file(path: Path, model: Mapping[str, Any], require_checksum: bool = True) -> dict[str, Any]:
    if not path.is_file():
        raise LabError(f"model file not found: {path}; run `qwen-lab fetch {model['id']}`")
    actual_size = path.stat().st_size
    expected_size = model.get("size_bytes")
    if expected_size is not None and actual_size != expected_size:
        raise LabError(f"size mismatch for {path}: expected {expected_size}, got {actual_size}")
    expected_sha = str(model.get("sha256") or "").lower()
    if require_checksum and not SHA256_RE.fullmatch(expected_sha):
        raise LabError(f"cannot verify {path}: lock has no resolved SHA-256")
    actual_sha = sha256_file(path)
    if expected_sha and SHA256_RE.fullmatch(expected_sha) and actual_sha != expected_sha:
        raise LabError(f"SHA-256 mismatch for {path}: expected {expected_sha}, got {actual_sha}")
    return {"path": str(path), "size_bytes": actual_size, "sha256": actual_sha}


def huggingface_url(model: Mapping[str, Any]) -> str:
    repository = str(model["repository"]).strip().rstrip("/")
    if repository.startswith("https://huggingface.co/"):
        repository = repository[len("https://huggingface.co/"):]
    elif "://" in repository:
        raise LabError(f"unsupported model repository: {repository}")
    if repository.endswith(".git"):
        repository = repository[:-4]
    if len(repository.split("/")) != 2 or any(part in {"", ".", ".."} for part in repository.split("/")):
        raise LabError(f"expected a Hugging Face owner/repository: {repository}")
    revision = urllib.parse.quote(str(model["revision"]), safe="")
    filename = "/".join(urllib.parse.quote(part, safe="") for part in Path(str(model["filename"])).parts)
    repo_path = "/".join(urllib.parse.quote(part, safe="") for part in repository.split("/"))
    return f"https://huggingface.co/{repo_path}/resolve/{revision}/{filename}?download=true"


def download_model(model: Mapping[str, Any], destination: Path, allow_unverified: bool) -> dict[str, Any]:
    if destination.exists():
        result = verify_model_file(destination, model, require_checksum=not allow_unverified)
        result["status"] = "already_present"
        return result
    destination.parent.mkdir(parents=True, exist_ok=True)
    partial = destination.with_name(f".{destination.name}.partial-{os.getpid()}")
    if partial.exists():
        raise LabError(f"refusing to overwrite partial download: {partial}")
    headers = {"User-Agent": "home-server-qwen-lab/1"}
    token = os.environ.get("HF_TOKEN")
    if token:
        headers["Authorization"] = f"Bearer {token}"
    request = urllib.request.Request(huggingface_url(model), headers=headers)
    digest = hashlib.sha256()
    total = 0
    try:
        with urllib.request.urlopen(request, timeout=60) as response, partial.open("xb") as output:
            while True:
                chunk = response.read(8 * 1024 * 1024)
                if not chunk:
                    break
                output.write(chunk)
                digest.update(chunk)
                total += len(chunk)
    except (OSError, urllib.error.URLError) as exc:
        try:
            partial.unlink()
        except OSError:
            pass
        raise LabError(f"download failed for {model['id']}: {exc}") from exc
    expected_size = model.get("size_bytes")
    expected_sha = str(model.get("sha256") or "").lower()
    actual_sha = digest.hexdigest()
    if expected_size is not None and total != expected_size:
        partial.unlink(missing_ok=True)
        raise LabError(f"download size mismatch for {model['id']}: expected {expected_size}, got {total}")
    if SHA256_RE.fullmatch(expected_sha) and actual_sha != expected_sha:
        partial.unlink(missing_ok=True)
        raise LabError(f"download SHA-256 mismatch for {model['id']}: expected {expected_sha}, got {actual_sha}")
    if not allow_unverified and not SHA256_RE.fullmatch(expected_sha):
        partial.unlink(missing_ok=True)
        raise LabError(f"refusing unverified download for {model['id']}")
    os.replace(partial, destination)
    return {"status": "downloaded", "path": str(destination), "size_bytes": total, "sha256": actual_sha}


def llama_build_config() -> dict[str, Any]:
    path = RUNTIME_CONFIG / "llama-cpp.lock.json"
    config = load_json(path)
    require_schema_one(config, path)
    return config


def build_paths(data_dir: Path, config: Mapping[str, Any]) -> tuple[Path, Path]:
    name = str(config["build"])
    arches = "-".join(f"sm{item}" for item in config["cuda"]["architectures"])
    source = data_dir / "sources" / f"llama.cpp-{name}"
    build = data_dir / "builds" / f"llama.cpp-{name}-cuda{config['cuda']['required_release']}-{arches}"
    return source, build


def build_plan(data_dir: Path, jobs: int) -> dict[str, Any]:
    config = llama_build_config()
    source, build = build_paths(data_dir, config)
    defines = dict(config["cmake_defines"])
    defines["CMAKE_CUDA_ARCHITECTURES"] = ";".join(config["cuda"]["architectures"])
    configure = ["cmake", "-S", str(source), "-B", str(build)]
    configure.extend(f"-D{key}={value}" for key, value in sorted(defines.items()))
    compile_command = [
        "cmake", "--build", str(build), "--config", "Release", "--parallel", str(jobs),
        "--target", *config["targets"],
    ]
    return {
        "config": config,
        "source_dir": str(source),
        "build_dir": str(build),
        "steps": [
            ["git", "clone", "--filter=blob:none", "--no-checkout", config["repository"], str(source)],
            ["git", "-C", str(source), "fetch", "origin", config["revision"]],
            ["git", "-C", str(source), "checkout", "--detach", "FETCH_HEAD"],
            configure,
            compile_command,
        ],
    }


def nvcc_release() -> str | None:
    record = run_capture(["nvcc", "--version"])
    combined = f"{record.get('stdout', '')}\n{record.get('stderr', '')}"
    match = re.search(r"release\s+(\d+\.\d+)", combined)
    return match.group(1) if match else None


def path_is_within(path: Path, root: Path) -> bool:
    try:
        path.resolve().relative_to(root.resolve())
        return True
    except ValueError:
        return False


def ldd_library_paths(output: str) -> dict[str, str]:
    libraries: dict[str, str] = {}
    for line in output.splitlines():
        match = re.match(r"^\s*(\S+)\s+=>\s+(/\S+)", line)
        if match:
            libraries[match.group(1)] = match.group(2)
            continue
        match = re.match(r"^\s*(/\S+)", line)
        if match:
            path = Path(match.group(1))
            libraries[path.name] = str(path)
    return libraries


def validate_dynamic_binary(
    binary: Path,
    project_root: Path | None = None,
    require_project_libraries: bool = False,
) -> dict[str, Any]:
    if not binary.is_file() or not os.access(binary, os.X_OK):
        raise LabError(f"built executable missing: {binary}")
    record = run_capture(["ldd", str(binary)], timeout=30)
    if record.get("returncode") != 0:
        raise LabError(f"cannot inspect linked libraries for {binary}: {record.get('stderr', record.get('error'))}")
    output = str(record.get("stdout", ""))
    if "not found" in output:
        raise LabError(f"unresolved shared library for {binary}:\n{output}")
    libraries = ldd_library_paths(output)
    project_libraries = {
        name: path for name, path in libraries.items()
        if re.match(r"^lib(?:llama|ggml)", name)
    }
    if require_project_libraries and not project_libraries:
        raise LabError(f"no dynamically linked llama.cpp project libraries found for {binary}")
    if project_root is not None:
        escaped = {
            name: path for name, path in project_libraries.items()
            if not path_is_within(Path(path), project_root)
        }
        if escaped:
            raise LabError(
                f"stale or external llama.cpp libraries resolved for {binary}: "
                f"{json.dumps(escaped, sort_keys=True)}"
            )
    return {
        "path": str(binary.resolve()),
        "sha256": sha256_file(binary),
        "ldd": output.splitlines(),
        "resolved_libraries": libraries,
        "project_libraries": project_libraries,
    }


def binary_version_attestation(binary: Path, config: Mapping[str, Any]) -> dict[str, Any]:
    expected_revision = str(config["revision"])
    expected_short = expected_revision[:7]
    expected_build_number = str(config["build"]).removeprefix("b")
    attempts: list[dict[str, Any]] = []
    for flag in ("--version", "--help"):
        record = run_capture([str(binary), flag], timeout=30)
        attempts.append(record)
        combined = f"{record.get('stdout', '')}\n{record.get('stderr', '')}"
        if expected_short in combined and re.search(rf"(?<!\d){re.escape(expected_build_number)}(?!\d)", combined):
            return {
                "path": str(binary.resolve()),
                "build": config["build"],
                "revision": expected_revision,
                "evidence_flag": flag,
                "evidence": combined.strip().splitlines()[:20],
            }
    raise LabError(
        f"runtime version attestation failed for {binary}: expected "
        f"{config['build']} / {expected_short} in --version or --help output"
    )


def validate_build(data_dir: Path, config: Mapping[str, Any]) -> dict[str, Any]:
    source, build = build_paths(data_dir, config)
    status = run_capture(["git", "-C", str(source), "status", "--porcelain"])
    if status.get("returncode") != 0 or status.get("stdout"):
        raise LabError(f"llama.cpp source is dirty or unreadable: {source}")
    head = run_capture(["git", "-C", str(source), "rev-parse", "HEAD"])
    revision = str(head.get("stdout", "")).strip()
    if head.get("returncode") != 0 or revision != str(config["revision"]):
        raise LabError(f"llama.cpp revision mismatch: expected {config['revision']}, got {revision or 'unknown'}")
    binaries = [
        validate_dynamic_binary(
            build / "bin" / target,
            project_root=build,
            require_project_libraries=True,
        )
        for target in config["targets"]
    ]
    cuda_libraries = sorted(build.rglob("libggml-cuda.so*"))
    if not cuda_libraries:
        raise LabError(f"CUDA shared library missing under {build}")
    cuda_link = validate_dynamic_binary(cuda_libraries[0], project_root=build)
    linked = "\n".join(cuda_link["ldd"])
    if "libcudart.so.12" not in linked:
        raise LabError("libggml-cuda is not linked to CUDA runtime major 12")
    cache_text = read_text(build / "CMakeCache.txt") or ""
    expected_arches = ";".join(config["cuda"]["architectures"])
    if not re.search(rf"CMAKE_CUDA_ARCHITECTURES:[^=]*={re.escape(expected_arches)}", cache_text):
        raise LabError(f"CMake cache does not contain CUDA architectures {expected_arches}")
    actual_cuda = nvcc_release()
    if actual_cuda != str(config["cuda"]["required_release"]):
        raise LabError(
            f"CUDA {config['cuda']['required_release']} runtime build required; detected nvcc {actual_cuda or 'none'}"
        )
    version_targets = {"llama-server", "llama-cli", "llama-perplexity"}
    versions = [
        binary_version_attestation(build / "bin" / target, config)
        for target in config["targets"] if target in version_targets
    ]
    validation = {
        "revision": revision,
        "build": config["build"],
        "cuda_release": actual_cuda,
        "cuda_architectures": config["cuda"]["architectures"],
        "binaries": binaries,
        "cuda_library": cuda_link,
        "versions": versions,
        "cmake_cache_sha256": sha256_file(build / "CMakeCache.txt"),
    }
    fingerprint_record = {
        "revision": validation["revision"],
        "build": validation["build"],
        "cuda_release": validation["cuda_release"],
        "cuda_architectures": validation["cuda_architectures"],
        "cmake_cache_sha256": validation["cmake_cache_sha256"],
        "binaries": [
            {
                "path": item["path"],
                "sha256": item["sha256"],
                "project_libraries": item["project_libraries"],
            }
            for item in validation["binaries"]
        ],
        "cuda_library": {
            "path": validation["cuda_library"]["path"],
            "sha256": validation["cuda_library"]["sha256"],
            "project_libraries": validation["cuda_library"]["project_libraries"],
        },
    }
    fingerprint_payload = json.dumps(fingerprint_record, sort_keys=True, separators=(",", ":")).encode("utf-8")
    validation["runtime_fingerprint"] = hashlib.sha256(fingerprint_payload).hexdigest()
    return validation


def execute_build(data_dir: Path, jobs: int) -> dict[str, Any]:
    plan = build_plan(data_dir, jobs)
    config = plan["config"]
    required_cuda = str(config["cuda"]["required_release"])
    actual_cuda = nvcc_release()
    if actual_cuda != required_cuda:
        raise LabError(f"CUDA {required_cuda} nvcc required; detected {actual_cuda or 'none'}")
    for command in ("git", "cmake", "ldd"):
        if shutil.which(command) is None:
            raise LabError(f"required command not found: {command}; this tool never installs packages")
    source = Path(plan["source_dir"])
    build = Path(plan["build_dir"])
    source.parent.mkdir(parents=True, exist_ok=True)
    build.parent.mkdir(parents=True, exist_ok=True)
    steps = plan["steps"]
    if not source.exists():
        run_checked(steps[0])
    if not (source / ".git").is_dir():
        raise LabError(f"source path exists but is not a Git checkout: {source}")
    status = run_capture(["git", "-C", str(source), "status", "--porcelain"])
    if status.get("returncode") != 0 or status.get("stdout"):
        raise LabError(f"refusing to alter dirty or unreadable managed source checkout: {source}")
    head = run_capture(["git", "-C", str(source), "rev-parse", "HEAD"])
    if not str(head.get("stdout", "")).strip().startswith(str(config["revision"])):
        run_checked(steps[1])
        run_checked(steps[2])
    run_checked(steps[3])
    run_checked(steps[4])
    validation = validate_build(data_dir, config)
    manifest = build / "qwen-lab-build.json"
    manifest.write_text(json.dumps(validation, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return {"status": "built", "manifest": str(manifest), "validation": validation}


def attest_runtime(data_dir: Path) -> dict[str, Any]:
    """Fail closed unless source, binaries, linked libraries and build identity match the lock."""
    return validate_build(data_dir, llama_build_config())


def sanity_config() -> dict[str, Any]:
    return load_runtime_config("sanity.json")


def sanity_config_sha256() -> str:
    return sha256_file(RUNTIME_CONFIG / "sanity.json")


def sanity_approval_path(data_dir: Path) -> Path:
    return data_dir / SANITY_APPROVAL


def detect_cuda_device(binary: Path) -> dict[str, Any]:
    record = run_capture([str(binary), "--list-devices"], timeout=60)
    if record.get("returncode") != 0:
        raise LabError(f"cannot list CUDA devices with {binary}: {record.get('stderr', record.get('error'))}")
    combined = f"{record.get('stdout', '')}\n{record.get('stderr', '')}"
    match = re.search(r"^\s*(CUDA\d+):\s*(.+)$", combined, re.MULTILINE)
    if not match:
        raise LabError("pinned llama-server did not expose a CUDA device; refusing CUDA sanity approval")
    return {"name": match.group(1), "description": match.group(2).strip(), "listing": combined.strip().splitlines()}


def sanity_server_argv(
    data_dir: Path,
    model: Mapping[str, Any],
    config: Mapping[str, Any],
    mode: str,
    host: str = "127.0.0.1",
    port: int = 18089,
    cuda_device: str = "CUDA0",
) -> list[str]:
    require_loopback(host)
    if not 1 <= int(port) <= 65535:
        raise LabError(f"invalid sanity server port: {port}")
    counts = thread_counts()
    threads = counts[str(config["threads_profile"])]
    argv = [
        str(binary_path(data_dir, "llama-server")),
        "--model", str(model_path(data_dir, model)),
        "--offline",
        "--no-mmproj",
        "--host", host,
        "--port", str(port),
        "--no-webui",
        "--ctx-size", str(config["ctx_size"]),
        "--parallel", "1",
        "--threads", str(threads),
        "--threads-batch", str(threads),
        "--batch-size", "128",
        "--ubatch-size", "64",
        "--cache-type-k", "q8_0",
        "--cache-type-v", "q8_0",
        "--reasoning", "off",
        "--no-cache-prompt",
        "--cache-ram", "0",
        "--no-cache-idle-slots",
        "--cache-reuse", "0",
        "--flash-attn", "off",
        "--load-mode", "mmap",
    ]
    if mode == "cpu":
        argv.extend([
            "--device", "none",
            "--n-gpu-layers", "0",
            "--no-kv-offload",
            "--no-op-offload",
            "--fit", "off",
        ])
    elif mode == "cuda":
        argv.extend([
            "--device", cuda_device,
            "--n-gpu-layers", str(config["gpu_layers"]),
            "--kv-offload",
            "--op-offload",
            "--fit", "on",
            "--fit-target", str(config["fit_target_mib"]),
        ])
    else:
        raise LabError(f"unknown sanity mode: {mode}")
    return argv


def sanity_completion_request(config: Mapping[str, Any]) -> dict[str, Any]:
    """Return the exact native /completion request used for both sanity legs."""
    return {
        "prompt": str(config["prompt"]),
        "n_predict": int(config["predict_tokens"]),
        "temperature": float(config["temperature"]),
        "samplers": ["temperature"],
        "seed": int(config["seed"]),
        "cache_prompt": False,
        "return_tokens": True,
        "stream": False,
    }


def reserve_loopback_port() -> int:
    """Ask the kernel for a presently free IPv4 loopback port."""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


def http_json(
    url: str,
    payload: Mapping[str, Any] | None = None,
    timeout: float = 5.0,
) -> tuple[int, Any]:
    """Issue a local JSON request and preserve HTTP error status/bodies for readiness polling."""
    data = None if payload is None else json.dumps(payload).encode("utf-8")
    headers = {"Accept": "application/json"}
    if data is not None:
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=data, headers=headers, method="GET" if data is None else "POST")
    try:
        response = NO_PROXY_OPENER.open(request, timeout=timeout)
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode("utf-8", errors="replace")
        try:
            body: Any = json.loads(raw)
        except json.JSONDecodeError:
            body = raw
        return int(exc.code), body
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        raise LabError(f"local HTTP request failed for {url}: {exc}") from exc
    with response:
        try:
            raw = response.read().decode("utf-8", errors="strict")
        except UnicodeDecodeError as exc:
            raise LabError(f"local endpoint returned invalid UTF-8 data for {url}") from exc
        try:
            body = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise LabError(f"local endpoint returned non-JSON data for {url}") from exc
        return int(response.status), body


def wait_for_sanity_server(
    process: subprocess.Popen[str],
    health_url: str,
    timeout_seconds: float,
) -> dict[str, Any]:
    """Wait until b10549 reports its documented 200/{status:ok} ready state."""
    deadline = time.monotonic() + timeout_seconds
    attempts = 0
    last_observation = "not contacted"
    while time.monotonic() < deadline:
        attempts += 1
        returncode = process.poll()
        if returncode is not None:
            raise LabError(f"llama-server exited before readiness with status {returncode}")
        try:
            status, body = http_json(health_url, timeout=min(2.0, timeout_seconds))
            last_observation = f"HTTP {status}: {body!r}"
            if status == 200 and isinstance(body, dict) and body.get("status") == "ok":
                return {"attempts": attempts, "status_code": status, "response": body}
        except LabError as exc:
            last_observation = str(exc)
        time.sleep(0.25)
    raise LabError(f"llama-server readiness timed out after {timeout_seconds:g}s ({last_observation})")


def stop_sanity_server(process: subprocess.Popen[str], timeout_seconds: float = 15.0) -> dict[str, Any]:
    """Send one SIGTERM and bound cleanup; never leave a failed sanity server behind."""
    returncode = process.poll()
    if returncode is not None:
        return {"method": "already_exited", "returncode": returncode, "clean": False}
    process.terminate()
    try:
        returncode = process.wait(timeout=timeout_seconds)
        return {"method": "sigterm", "returncode": returncode, "clean": returncode == 0}
    except subprocess.TimeoutExpired:
        process.kill()
        returncode = process.wait(timeout=5)
        return {"method": "sigkill_after_timeout", "returncode": returncode, "clean": False}


def clean_generation(text: str) -> str:
    return " ".join(ANSI_RE.sub("", text).replace("\x00", "").split())


def generation_checks(text: str, config: Mapping[str, Any]) -> dict[str, Any]:
    cleaned = clean_generation(text.replace(str(config["prompt"]), ""))
    printable = sum(1 for char in cleaned if char.isprintable())
    ratio = printable / len(cleaned) if cleaned else 0.0
    marker_count = cleaned.count(str(config["required_marker"]))
    checks = {
        "non_empty": bool(cleaned),
        "required_marker": 1 <= marker_count <= 2,
        "finite": BAD_FLOAT_RE.search(cleaned) is None,
        "no_nul": "\x00" not in text,
        "no_replacement_character": "\ufffd" not in cleaned,
        "printable_ratio": ratio >= float(config["minimum_printable_ratio"]),
        "bounded_output": len(cleaned) <= 2048,
    }
    return {"passed": all(checks.values()), "checks": checks, "printable_ratio": ratio, "text": cleaned}


def run_sanity_server_leg(
    data_dir: Path,
    model: Mapping[str, Any],
    config: Mapping[str, Any],
    mode: str,
    cuda_device: str = "CUDA0",
) -> dict[str, Any]:
    """Run one isolated ephemeral server, make one deterministic request, then stop it."""
    started = time.monotonic()
    host = "127.0.0.1"
    port = reserve_loopback_port()
    argv = sanity_server_argv(data_dir, model, config, mode, host, port, cuda_device)
    base_url = f"http://{host}:{port}"
    request_payload = sanity_completion_request(config)
    response_body: Any = None
    readiness: dict[str, Any] | None = None
    request_error: str | None = None
    shutdown: dict[str, Any] = {"method": "not_started", "returncode": None, "clean": False}
    log_text = ""
    try:
        with tempfile.TemporaryFile(mode="w+", encoding="utf-8") as server_log:
            try:
                process = subprocess.Popen(
                    argv,
                    stdout=server_log,
                    stderr=subprocess.STDOUT,
                    text=True,
                )
            except (FileNotFoundError, OSError) as exc:
                raise LabError(f"cannot start sanity server {argv[0]}: {exc}") from exc
            try:
                readiness = wait_for_sanity_server(
                    process,
                    f"{base_url}/health",
                    float(config["startup_timeout_seconds"]),
                )
                status, response_body = http_json(
                    f"{base_url}/completion",
                    request_payload,
                    timeout=float(config["request_timeout_seconds"]),
                )
                if status != 200:
                    raise LabError(f"/completion returned HTTP {status}: {response_body!r}")
            except LabError as exc:
                request_error = str(exc)
            finally:
                shutdown = stop_sanity_server(process, float(config["shutdown_timeout_seconds"]))
                server_log.flush()
                server_log.seek(0)
                log_text = server_log.read()
    except LabError:
        raise

    response_object = response_body if isinstance(response_body, dict) else {}
    content = response_object.get("content") if isinstance(response_object.get("content"), str) else ""
    raw_tokens = response_object.get("tokens")
    tokens = raw_tokens if isinstance(raw_tokens, list) else []
    tokens_valid = bool(tokens) and all(isinstance(token, int) and not isinstance(token, bool) for token in tokens)
    response_checks = {
        "object": isinstance(response_body, dict),
        "content_string": isinstance(response_object.get("content"), str),
        "tokens_non_empty_integers": tokens_valid,
        "tokens_bounded": tokens_valid and len(tokens) <= int(config["predict_tokens"]),
    }
    checks = generation_checks(content, config)
    runtime_checks = {
        "finite_logs": BAD_FLOAT_RE.search(log_text) is None,
        "no_oom": "out of memory" not in log_text.lower(),
        "no_cuda_error": "cuda error" not in log_text.lower(),
        "clean_shutdown": bool(shutdown["clean"]),
    }
    return {
        "argv": argv,
        "endpoint": f"{base_url}/completion",
        "elapsed_seconds": time.monotonic() - started,
        "request": request_payload,
        "response": response_body,
        "tokens": tokens,
        "readiness": readiness,
        "request_error": request_error,
        "shutdown": shutdown,
        "server_log_tail": log_text[-65536:],
        "generation": checks,
        "response_checks": response_checks,
        "runtime_checks": runtime_checks,
        "passed": (
            request_error is None
            and checks["passed"]
            and all(response_checks.values())
            and all(runtime_checks.values())
        ),
    }


def sanity_models(models: Mapping[str, dict[str, Any]]) -> dict[str, Any]:
    candidates = models_with_role(models, SANITY_ROLE)
    if len(candidates) != 1:
        raise LabError(f"model lock must contain exactly one {SANITY_ROLE!r} artifact, found {len(candidates)}")
    model = candidates[0]
    require_executable_model(model)
    return model


def run_sanity(
    data_dir: Path,
    models: Mapping[str, dict[str, Any]],
    approve: bool,
) -> dict[str, Any]:
    config = sanity_config()
    model = sanity_models(models)
    verification = verify_model_file(model_path(data_dir, model), model)
    attestation = attest_runtime(data_dir)
    server = binary_path(data_dir, "llama-server")
    cuda = detect_cuda_device(server)
    cpu = run_sanity_server_leg(data_dir, model, config, "cpu")
    cuda_leg = run_sanity_server_leg(data_dir, model, config, "cuda", cuda["name"])
    cpu_text = cpu["generation"]["text"]
    cuda_text = cuda_leg["generation"]["text"]
    similarity = difflib.SequenceMatcher(None, cpu_text, cuda_text).ratio()
    tokens_equal = bool(cpu["tokens"]) and cpu["tokens"] == cuda_leg["tokens"]
    coherent = (
        cpu["passed"]
        and cuda_leg["passed"]
        and (tokens_equal or not bool(config["require_exact_token_match"]))
        and similarity >= float(config["minimum_similarity_ratio"])
    )
    report = {
        "schema_version": 1,
        "captured_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "approved": bool(approve and coherent),
        "model_id": model["id"],
        "model_sha256": verification["sha256"],
        "sanity_config_sha256": sanity_config_sha256(),
        "runtime_revision": attestation["revision"],
        "runtime_build": attestation["build"],
        "runtime_fingerprint": attestation["runtime_fingerprint"],
        "cuda_device": cuda,
        "cpu": cpu,
        "cuda": cuda_leg,
        "token_ids_equal": tokens_equal,
        "similarity_ratio": similarity,
        "passed": coherent,
    }
    if not coherent:
        raise LabError("CPU/CUDA Gated DeltaNet sanity failed; no approval was written")
    if approve:
        atomic_json(sanity_approval_path(data_dir), report)
        report["approval_path"] = str(sanity_approval_path(data_dir))
    return report


def require_sanity_approval(
    data_dir: Path,
    models: Mapping[str, dict[str, Any]],
    attestation: Mapping[str, Any],
) -> dict[str, Any]:
    path = sanity_approval_path(data_dir)
    approval = load_json(path)
    require_schema_one(approval, path)
    model = sanity_models(models)
    expected = {
        "approved": True,
        "model_id": model["id"],
        "model_sha256": str(model["sha256"]).lower(),
        "sanity_config_sha256": sanity_config_sha256(),
        "runtime_revision": attestation["revision"],
        "runtime_build": attestation["build"],
        "runtime_fingerprint": attestation["runtime_fingerprint"],
        "passed": True,
    }
    mismatches = {
        key: {"expected": value, "actual": approval.get(key)}
        for key, value in expected.items() if approval.get(key) != value
    }
    if mismatches:
        raise LabError(
            f"Gated DeltaNet sanity approval is stale or invalid: {json.dumps(mismatches, sort_keys=True)}; "
            "rerun `qwen-lab sanity --approve`"
        )
    return approval


def thread_counts() -> dict[str, int]:
    cpu = cpu_snapshot()
    return {"physical": int(cpu["physical_cores"]), "logical": int(cpu["logical_cores"])}


def require_loopback(host: str) -> None:
    try:
        if not ipaddress.ip_address(host).is_loopback:
            raise LabError(f"refusing non-loopback server bind: {host}")
    except ValueError as exc:
        raise LabError(f"server host must be a numeric loopback address, got {host!r}") from exc


def select_model(models: Mapping[str, dict[str, Any]], model_id: str) -> dict[str, Any]:
    try:
        return models[model_id]
    except KeyError as exc:
        raise LabError(f"unknown model id {model_id!r}; choices: {', '.join(sorted(models))}") from exc


def binary_path(data_dir: Path, target: str) -> Path:
    override = os.environ.get(f"QWEN_LAB_{target.upper().replace('-', '_')}")
    if override:
        return Path(override).expanduser().resolve()
    config = llama_build_config()
    _, build = build_paths(data_dir, config)
    return build / "bin" / target


def optimization_profiles() -> dict[str, Any]:
    config = load_runtime_config("optimization-profiles.json")
    profiles = config.get("profiles")
    if not isinstance(profiles, dict) or not profiles:
        raise LabError("optimization-profiles.json must contain non-empty profiles")
    if config.get("default_profile") not in profiles:
        raise LabError("optimization profile default is not present")
    minimum = config.get("minimum_speedup_percent")
    if not isinstance(minimum, (int, float)) or isinstance(minimum, bool) or minimum < 10:
        raise LabError("optimization profiles must require at least 10 percent speedup")
    return config


def server_plan(
    data_dir: Path,
    model: Mapping[str, Any],
    port: int | None = None,
    optimization_profile: str | None = None,
) -> dict[str, Any]:
    profile_path = RUNTIME_CONFIG / "server-baseline.json"
    profile = load_json(profile_path)
    require_schema_one(profile, profile_path)
    host = str(profile["host"])
    require_loopback(host)
    selected_port = int(port if port is not None else profile["port"])
    if not 1 <= selected_port <= 65535:
        raise LabError(f"invalid TCP port: {selected_port}")
    counts = thread_counts()
    threads = counts[str(profile["thread_profile"])]
    optimization = optimization_profiles()
    profile_name = optimization_profile or str(optimization["default_profile"])
    try:
        selected_optimization = optimization["profiles"][profile_name]
    except KeyError as exc:
        raise LabError(
            f"unknown optimization profile {profile_name!r}; choices: "
            f"{', '.join(sorted(optimization['profiles']))}"
        ) from exc
    command = [
        str(binary_path(data_dir, "llama-server")),
        "--model", str(model_path(data_dir, model)),
        "--offline",
        "--no-mmproj",
        "--host", host,
        "--port", str(selected_port),
        "--ctx-size", str(profile["ctx_size"]),
        "--parallel", str(profile["parallel"]),
        "--threads", str(threads),
        "--threads-batch", str(threads),
        "--batch-size", str(profile["batch_size"]),
        "--ubatch-size", str(profile["ubatch_size"]),
        "--cache-type-k", str(selected_optimization["cache_type_k"]),
        "--cache-type-v", str(selected_optimization["cache_type_v"]),
        "--flash-attn", str(profile["flash_attention"]),
        "--n-gpu-layers", str(profile["gpu_layers"]),
        "--fit", "on",
        "--fit-target", str(profile["fit_target_mib"]),
        "--load-mode", str(profile["load_mode"]),
        "--no-webui",
    ]
    if profile["kv_location"] == "cpu":
        command.append("--no-kv-offload")
    if profile.get("metrics"):
        command.append("--metrics")
    command.extend(str(item) for item in selected_optimization.get("server_args", []))
    return {
        "profile": profile,
        "optimization_profile": profile_name,
        "optimization": selected_optimization,
        "minimum_speedup_percent": optimization["minimum_speedup_percent"],
        "model_id": model["id"],
        "argv": command,
    }


def matrix_cases(config: Mapping[str, Any], counts: Mapping[str, int]) -> list[dict[str, Any]]:
    common = itertools.product(
        config["context_depths"],
        config["thread_profiles"],
        config["ubatch_sizes"],
        config["cache_types"],
        config["flash_attention"],
    )
    base = list(common)
    cases: list[dict[str, Any]] = []
    if "cpu" in config["modes"]:
        for context, thread_profile, ubatch, cache, fa in base:
            cases.append({
                "mode": "cpu", "context_depth": context, "thread_profile": thread_profile,
                "threads": counts[thread_profile], "ubatch_size": ubatch, "cache_type": cache,
                "kv_location": "cpu", "flash_attention": fa, "gpu_layers": 0,
            })
    if "cuda" in config["modes"]:
        layers: list[int | str] = []
        if config["gpu_layers"].get("auto"):
            layers.append("auto")
        layers.extend(config["gpu_layers"]["sweep"])
        for values in base:
            context, thread_profile, ubatch, cache, fa = values
            for kv_location, gpu_layers in itertools.product(config["kv_locations"], layers):
                cases.append({
                    "mode": "cuda", "context_depth": context, "thread_profile": thread_profile,
                    "threads": counts[thread_profile], "ubatch_size": ubatch, "cache_type": cache,
                    "kv_location": kv_location, "flash_attention": fa, "gpu_layers": gpu_layers,
                })
    return cases


def bench_argv(data_dir: Path, model: Mapping[str, Any], config: Mapping[str, Any], case: Mapping[str, Any]) -> list[str]:
    argv = [
        str(binary_path(data_dir, "llama-bench")),
        "--model", str(model_path(data_dir, model)),
        "--offline",
        "--n-prompt", str(config["prompt_tokens"]),
        "--n-gen", str(config["generation_tokens"]),
        "--n-depth", str(case["context_depth"]),
        "--batch-size", str(config["batch_size"]),
        "--ubatch-size", str(case["ubatch_size"]),
        "--cache-type-k", str(case["cache_type"]),
        "--cache-type-v", str(case["cache_type"]),
        "--threads", str(case["threads"]),
        "--no-kv-offload", "1" if case["kv_location"] == "cpu" else "0",
        "--flash-attn", str(case["flash_attention"]),
        "--load-mode", str(config["load_mode"]),
        "--repetitions", str(config["repetitions"]),
        "--output", "json",
    ]
    mode = case.get("mode", "cpu" if case.get("gpu_layers") == 0 else "cuda")
    if mode == "cpu":
        argv.extend(["--device", "none", "--no-op-offload", "1"])
    if case["gpu_layers"] == "auto":
        argv.extend([
            "--n-gpu-layers", "-1",
            "--fit-target", str(config["gpu_layers"]["fit_target_mib"]),
            "--fit-ctx", str(case["context_depth"]),
        ])
    else:
        argv.extend(["--n-gpu-layers", str(case["gpu_layers"])])
    return argv


def bench_plan(data_dir: Path, model: Mapping[str, Any]) -> dict[str, Any]:
    path = RUNTIME_CONFIG / "bench-matrix.json"
    config = load_json(path)
    require_schema_one(config, path)
    counts = thread_counts()
    cases = matrix_cases(config, counts)
    planned_cases = []
    for index, case in enumerate(cases):
        planned_cases.append(dict(case, global_case_index=index, argv=bench_argv(data_dir, model, config, case)))
    return {
        "model_id": model["id"],
        "thread_counts": counts,
        "case_count": len(cases),
        "case_timeout_seconds": config["case_timeout_seconds"],
        "telemetry": config["telemetry"],
        "continue_after_timeout": config["continue_after_timeout"],
        "continue_after_memory_elimination": config["continue_after_memory_elimination"],
        "cases": planned_cases,
    }


def parse_bench_metrics(stdout: str, expected_revision: str, expected_build: str) -> list[dict[str, Any]]:
    try:
        payload = json.loads(stdout)
    except json.JSONDecodeError as exc:
        raise LabError(f"llama-bench returned invalid JSON: {exc}") from exc
    if not isinstance(payload, list) or not payload:
        raise LabError("llama-bench JSON must be a non-empty array")
    normalized: list[dict[str, Any]] = []
    expected_number = int(str(expected_build).removeprefix("b"))
    for index, row in enumerate(payload):
        if not isinstance(row, dict):
            raise LabError(f"llama-bench row {index} is not an object")
        commit = str(row.get("build_commit", ""))
        if not expected_revision.startswith(commit) or len(commit) < 7:
            raise LabError(
                f"llama-bench row {index} commit mismatch: expected {expected_revision}, got {commit or 'missing'}"
            )
        if int(row.get("build_number", -1)) != expected_number:
            raise LabError(
                f"llama-bench row {index} build mismatch: expected {expected_number}, "
                f"got {row.get('build_number')}"
            )
        samples = row.get("samples_ts")
        if not isinstance(samples, list) or not samples:
            raise LabError(f"llama-bench row {index} has no samples_ts")
        try:
            numeric_samples = [float(value) for value in samples]
        except (TypeError, ValueError) as exc:
            raise LabError(f"llama-bench row {index} has invalid samples_ts") from exc
        if any(value <= 0 or value != value for value in numeric_samples):
            raise LabError(f"llama-bench row {index} contains non-positive or NaN throughput")
        normalized.append({
            "test": "prompt" if int(row.get("n_prompt", 0)) > 0 else "decode",
            "n_prompt": int(row.get("n_prompt", 0)),
            "n_gen": int(row.get("n_gen", 0)),
            "n_depth": int(row.get("n_depth", 0)),
            "median_tokens_per_second": statistics.median(numeric_samples),
            "mean_tokens_per_second": statistics.fmean(numeric_samples),
            "samples_tokens_per_second": numeric_samples,
            "build_commit": commit,
            "build_number": expected_number,
        })
    return normalized


def classify_bench_failure(returncode: int, stdout: str, stderr: str, config: Mapping[str, Any]) -> str:
    if returncode == 0:
        return "success"
    combined = f"{stdout}\n{stderr}".lower()
    if any(str(pattern).lower() in combined for pattern in config["expected_failure_patterns"]):
        return "expected_failure"
    return "unexpected_failure"


def parse_vmstat() -> dict[str, int]:
    counters: dict[str, int] = {}
    for line in (read_text(Path("/proc/vmstat")) or "").splitlines():
        fields = line.split()
        if len(fields) == 2 and fields[1].isdigit():
            counters[fields[0]] = int(fields[1])
    return counters


def process_memory_sample(pid: int) -> dict[str, int] | None:
    status = read_text(Path(f"/proc/{pid}/status"))
    stat = read_text(Path(f"/proc/{pid}/stat"))
    if status is None and stat is None:
        return None
    result: dict[str, int] = {}
    for line in (status or "").splitlines():
        match = re.match(r"^(VmRSS|VmHWM|VmSwap):\s+(\d+)\s+kB$", line)
        if match:
            result[f"{match.group(1)}_bytes"] = int(match.group(2)) * 1024
    if stat:
        closing = stat.rfind(")")
        fields = stat[closing + 2:].split() if closing >= 0 else []
        if len(fields) > 9:
            try:
                result["major_faults"] = int(fields[9])
            except ValueError:
                pass
    return result


def process_gpu_memory_mib(pid: int) -> int | None:
    query = run_capture([
        "nvidia-smi",
        "--query-compute-apps=pid,used_gpu_memory",
        "--format=csv,noheader,nounits",
    ], timeout=5)
    if query.get("returncode") != 0:
        return None
    total = 0
    found = False
    for line in str(query.get("stdout", "")).splitlines():
        values = [value.strip() for value in line.split(",", 1)]
        if len(values) != 2 or values[0] != str(pid):
            continue
        try:
            total += int(values[1])
            found = True
        except ValueError:
            continue
    return total if found else 0


def bench_telemetry_sample(pid: int, started: float, include_gpu: bool) -> dict[str, Any]:
    memory = parse_meminfo()
    vmstat = parse_vmstat()
    swap_total = memory.get("SwapTotal_bytes")
    swap_free = memory.get("SwapFree_bytes")
    swap_used = None
    if swap_total is not None and swap_free is not None:
        swap_used = max(0, swap_total - swap_free)
    return {
        "elapsed_seconds": max(0.0, time.monotonic() - started),
        "system": {
            "memory_available_bytes": memory.get("MemAvailable_bytes"),
            "swap_used_bytes": swap_used,
            "major_faults": vmstat.get("pgmajfault"),
            "swapin_pages": vmstat.get("pswpin"),
        },
        "process": process_memory_sample(pid),
        "gpu_process_memory_mib": process_gpu_memory_mib(pid) if include_gpu else None,
    }


def _known_numbers(samples: Sequence[Mapping[str, Any]], *keys: str) -> list[float]:
    values: list[float] = []
    for sample in samples:
        value: Any = sample
        for key in keys:
            value = value.get(key) if isinstance(value, Mapping) else None
        if isinstance(value, (int, float)) and not isinstance(value, bool):
            values.append(float(value))
    return values


def _sustained_rate(
    samples: Sequence[Mapping[str, Any]],
    keys: Sequence[str],
    limit: float,
    consecutive_required: int,
    multiplier: float = 1.0,
) -> dict[str, Any]:
    rates: list[float] = []
    for previous, current in zip(samples, samples[1:]):
        before: Any = previous
        after: Any = current
        for key in keys:
            before = before.get(key) if isinstance(before, Mapping) else None
            after = after.get(key) if isinstance(after, Mapping) else None
        elapsed = float(current["elapsed_seconds"]) - float(previous["elapsed_seconds"])
        if not isinstance(before, (int, float)) or not isinstance(after, (int, float)) or elapsed <= 0:
            continue
        rates.append(max(0.0, float(after) - float(before)) * multiplier / elapsed)
    longest = 0
    current_run = 0
    for rate in rates:
        current_run = current_run + 1 if rate > limit else 0
        longest = max(longest, current_run)
    return {
        "known": bool(rates),
        "maximum_rate": max(rates) if rates else None,
        "longest_over_limit_intervals": longest,
        "limit": limit,
        "sustained": longest >= consecutive_required,
    }


def summarize_bench_telemetry(
    samples: Sequence[Mapping[str, Any]],
    telemetry_config: Mapping[str, Any],
) -> dict[str, Any]:
    warmup = float(telemetry_config["warmup_grace_seconds"])
    post = [sample for sample in samples if float(sample["elapsed_seconds"]) >= warmup]
    minimum_samples = int(telemetry_config["minimum_post_warmup_samples"])
    enough = len(post) >= minimum_samples
    evaluated = post if enough else []
    swap_values = _known_numbers(evaluated, "system", "swap_used_bytes")
    process_swap = _known_numbers(evaluated, "process", "VmSwap_bytes")
    available = _known_numbers(evaluated, "system", "memory_available_bytes")
    rss = _known_numbers(samples, "process", "VmRSS_bytes")
    gpu = _known_numbers(samples, "gpu_process_memory_mib")
    swap_growth = max(swap_values) - swap_values[0] if swap_values else None
    major_rate = _sustained_rate(
        evaluated,
        ("process", "major_faults"),
        float(telemetry_config["max_major_fault_rate_per_second"]),
        int(telemetry_config["sustained_intervals"]),
    )
    swapin_rate = _sustained_rate(
        evaluated,
        ("system", "swapin_pages"),
        float(telemetry_config["max_swapin_bytes_per_second"]),
        int(telemetry_config["sustained_intervals"]),
        float(os.sysconf("SC_PAGE_SIZE")),
    )
    checks = {
        "post_warmup_samples": {
            "status": "pass" if enough else "unknown",
            "actual": len(post),
            "minimum": minimum_samples,
        },
        "swap_growth": {
            "status": "unknown" if swap_growth is None else (
                "pass" if swap_growth <= int(telemetry_config["max_swap_growth_bytes"]) else "fail"
            ),
            "actual_bytes": swap_growth,
            "maximum_bytes": int(telemetry_config["max_swap_growth_bytes"]),
        },
        "process_swap": {
            "status": "unknown" if not process_swap else (
                "pass" if max(process_swap) <= int(telemetry_config["max_process_swap_bytes"]) else "fail"
            ),
            "peak_bytes": max(process_swap) if process_swap else None,
            "maximum_bytes": int(telemetry_config["max_process_swap_bytes"]),
        },
        "available_ram": {
            "status": "unknown" if not available else (
                "pass" if min(available) >= int(telemetry_config["minimum_available_ram_bytes"]) else "fail"
            ),
            "minimum_observed_bytes": min(available) if available else None,
            "minimum_required_bytes": int(telemetry_config["minimum_available_ram_bytes"]),
        },
        "sustained_major_faults": {
            "status": "unknown" if not major_rate["known"] else ("fail" if major_rate["sustained"] else "pass"),
            **major_rate,
        },
        "sustained_swapin": {
            "status": "unknown" if not swapin_rate["known"] else ("fail" if swapin_rate["sustained"] else "pass"),
            **swapin_rate,
        },
    }
    statuses = [check["status"] for check in checks.values()]
    verdict = "fail" if "fail" in statuses else ("unknown" if "unknown" in statuses else "pass")
    return {
        "verdict": verdict,
        "sample_count": len(samples),
        "post_warmup_sample_count": len(post),
        "warmup_grace_seconds": warmup,
        "peak_process_rss_bytes": max(rss) if rss else None,
        "peak_process_gpu_memory_mib": max(gpu) if gpu else None,
        "checks": checks,
    }


def run_bench_process(
    argv: Sequence[str],
    timeout_seconds: float,
    telemetry_config: Mapping[str, Any],
    include_gpu: bool,
) -> dict[str, Any]:
    """Run llama-bench while sampling memory pressure without extra Python dependencies."""
    try:
        process = subprocess.Popen(list(argv), stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    except (FileNotFoundError, OSError) as exc:
        raise LabError(f"cannot start benchmark executable {argv[0]}: {exc}") from exc
    started = time.monotonic()
    deadline = started + timeout_seconds
    interval = max(0.25, float(telemetry_config["sample_interval_seconds"]))
    samples: list[dict[str, Any]] = []
    timed_out = False
    while True:
        samples.append(bench_telemetry_sample(process.pid, started, include_gpu))
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            timed_out = True
            process.kill()
            stdout, stderr = process.communicate()
            break
        try:
            stdout, stderr = process.communicate(timeout=min(interval, remaining))
            break
        except subprocess.TimeoutExpired:
            continue
    telemetry = summarize_bench_telemetry(samples, telemetry_config)
    return {
        "returncode": None if timed_out else process.returncode,
        "stdout": stdout or "",
        "stderr": stderr or "",
        "timed_out": timed_out,
        "telemetry": telemetry,
    }


def atomic_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.tmp-{os.getpid()}")
    temporary.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.replace(temporary, path)


def command_probe(args: argparse.Namespace) -> int:
    snapshot = hardware_snapshot(data_dir_from_args(args))
    if args.disk_throughput_file:
        requirements = load_runtime_config("requirements.json")
        limit = args.disk_throughput_bytes or int(requirements["disk_probe_bytes"])
        snapshot["storage"]["read_throughput_probe"] = disk_read_probe(
            Path(args.disk_throughput_file),
            limit,
            int(requirements["disk_probe_block_bytes"]),
        )
    if args.output:
        atomic_json(Path(args.output).expanduser().resolve(), snapshot)
    print(json.dumps(snapshot, indent=2, sort_keys=True))
    return 0


def command_doctor(args: argparse.Namespace) -> int:
    snapshot = hardware_snapshot(data_dir_from_args(args))
    result = {"snapshot": snapshot, "verdict": doctor_verdict(snapshot)}
    if args.output:
        atomic_json(Path(args.output).expanduser().resolve(), result)
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0 if result["verdict"]["status"] == "pass" else 3


def command_build(args: argparse.Namespace) -> int:
    data_dir = data_dir_from_args(args)
    plan = build_plan(data_dir, args.jobs)
    if args.dry_run:
        print(json.dumps(plan, indent=2, sort_keys=True))
        return 0
    print(json.dumps(execute_build(data_dir, args.jobs), indent=2, sort_keys=True))
    return 0


def command_fetch(args: argparse.Namespace) -> int:
    data_dir = data_dir_from_args(args)
    models = load_models(model_lock_path(args), allow_unverified=args.allow_unverified)
    if args.list:
        print(json.dumps({key: value for key, value in models.items()}, indent=2, sort_keys=True))
        return 0
    ids = sorted(models) if args.all else args.model_ids
    if not ids:
        raise LabError("select one or more MODEL_ID values, or pass --all/--list")
    selected = [select_model(models, item) for item in ids]
    plan = [
        {"model_id": model["id"], "url": huggingface_url(model), "destination": str(model_path(data_dir, model))}
        for model in selected
    ]
    if args.dry_run:
        print(json.dumps({"dry_run": True, "downloads": plan}, indent=2, sort_keys=True))
        return 0
    pending_bytes = sum(
        int(model.get("size_bytes") or 0)
        for model in selected if not model_path(data_dir, model).exists()
    )
    space = require_free_space(data_dir, pending_bytes, DOWNLOAD_RESERVE_BYTES)
    if args.allow_unverified:
        print("WARNING: downloading without complete immutable verification", file=sys.stderr)
    results = [download_model(model, model_path(data_dir, model), args.allow_unverified) for model in selected]
    print(json.dumps({"space_preflight": space, "results": results}, indent=2, sort_keys=True))
    return 0


def command_sanity(args: argparse.Namespace) -> int:
    data_dir = data_dir_from_args(args)
    models = load_models(model_lock_path(args), allow_unverified=False)
    model = sanity_models(models)
    if args.dry_run:
        config = sanity_config()
        request = sanity_completion_request(config)
        plan = {
            "model_id": model["id"],
            "approval_path": str(sanity_approval_path(data_dir)),
            "approve_requested": args.approve,
            "transport": config["transport"],
            "request": request,
            "ports_are_dry_run_examples": True,
            "cpu_server_argv": sanity_server_argv(data_dir, model, config, "cpu", port=18089),
            "cuda_server_argv": sanity_server_argv(data_dir, model, config, "cuda", port=18090),
            "health_path": "/health",
            "completion_path": "/completion",
        }
        print(json.dumps(dict(plan, dry_run=True), indent=2, sort_keys=True))
        return 0
    report = run_sanity(data_dir, models, args.approve)
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0


def command_serve(args: argparse.Namespace) -> int:
    data_dir = data_dir_from_args(args)
    models = load_models(model_lock_path(args), allow_unverified=False)
    model = select_model(models, args.model_id)
    require_executable_model(model)
    plan = server_plan(data_dir, model, args.port, args.optimization_profile)
    if args.dry_run:
        print(json.dumps(dict(plan, dry_run=True), indent=2, sort_keys=True))
        return 0
    verify_model_file(model_path(data_dir, model), model)
    require_free_space(data_dir, 0, RUN_RESERVE_BYTES)
    attestation = attest_runtime(data_dir)
    if model.get("role") != SANITY_ROLE:
        require_sanity_approval(data_dir, models, attestation)
    server = Path(plan["argv"][0])
    print(f"Starting loopback-only llama-server: {shlex.join(plan['argv'])}", file=sys.stderr)
    os.execv(str(server), plan["argv"])
    raise AssertionError("unreachable")


def command_bench(args: argparse.Namespace) -> int:
    data_dir = data_dir_from_args(args)
    models = load_models(model_lock_path(args), allow_unverified=False)
    model = select_model(models, args.model_id)
    require_executable_model(model)
    plan = bench_plan(data_dir, model)
    cases = plan["cases"]
    if args.case is not None:
        if not 0 <= args.case < len(cases):
            raise LabError(f"--case must be between 0 and {len(cases) - 1}")
        cases = [cases[args.case]]
    if args.max_cases:
        cases = cases[:args.max_cases]
    plan["selected_case_count"] = len(cases)
    plan["cases"] = cases
    if args.dry_run:
        print(json.dumps(dict(plan, dry_run=True), indent=2, sort_keys=True))
        return 0
    if len(cases) > 16 and not args.confirm_full_matrix:
        raise LabError(
            f"refusing to start {len(cases)} expensive cases without --confirm-full-matrix; "
            "use --case or --max-cases for a bounded run"
        )
    verify_model_file(model_path(data_dir, model), model)
    require_free_space(data_dir, 0, RUN_RESERVE_BYTES)
    attestation = attest_runtime(data_dir)
    if model.get("role") != SANITY_ROLE:
        require_sanity_approval(data_dir, models, attestation)
    config = load_runtime_config("bench-matrix.json")
    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
    run_dir = data_dir / "benchmarks" / f"{stamp}-{model_slug(args.model_id)}"
    run_dir.mkdir(parents=True, exist_ok=False)
    atomic_json(run_dir / "plan.json", plan)
    results_path = run_dir / "results.jsonl"
    with results_path.open("x", encoding="utf-8") as output:
        counts = {
            "success": 0,
            "memory_eliminated": 0,
            "expected_failure": 0,
            "unexpected_failure": 0,
            "timeout": 0,
        }
        for case in cases:
            execution = run_bench_process(
                case["argv"],
                float(config["case_timeout_seconds"]),
                config["telemetry"],
                include_gpu=case["mode"] == "cuda",
            )
            timed_out = bool(execution["timed_out"])
            stdout = str(execution["stdout"])
            stderr = str(execution["stderr"])
            returncode = execution["returncode"]
            classification = "timeout" if timed_out else classify_bench_failure(
                int(returncode), stdout, stderr, config
            )
            record = {
                "case_index": case["global_case_index"],
                "case": {key: value for key, value in case.items() if key not in {"argv", "global_case_index"}},
                "argv": case["argv"],
                "returncode": returncode,
                "classification": classification,
                "stdout": stdout,
                "stderr": stderr,
                "telemetry": execution["telemetry"],
            }
            if classification == "success":
                record["metrics"] = parse_bench_metrics(
                    stdout,
                    str(attestation["revision"]),
                    str(attestation["build"]),
                )
                if execution["telemetry"]["verdict"] == "fail":
                    classification = "memory_eliminated"
                    record["classification"] = classification
            output.write(json.dumps(record, sort_keys=True) + "\n")
            output.flush()
            counts[classification] += 1
            if classification == "expected_failure" and config["continue_after_expected_failure"]:
                continue
            if classification == "timeout" and config["continue_after_timeout"]:
                continue
            if classification == "memory_eliminated" and config["continue_after_memory_elimination"]:
                continue
            if classification != "success":
                suffix = " timed out" if timed_out else " failed unexpectedly"
                raise LabError(
                    f"benchmark case {case['global_case_index']}{suffix}; partial results: {results_path}"
                )
    print(json.dumps({
        "status": "complete",
        "results": str(results_path),
        "case_count": len(cases),
        "classifications": counts,
    }, indent=2))
    return 0


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser(prog="qwen-lab", description=__doc__)
    root.add_argument(
        "--data-dir",
        help="lab state root (default: QWEN_LAB_DATA_DIR or /srv/qwen-lab)",
    )
    commands = root.add_subparsers(dest="command", required=True)

    probe = commands.add_parser("probe", help="print a read-only hardware snapshot")
    probe.add_argument("--output", help="also atomically write the snapshot to this path")
    probe.add_argument(
        "--disk-throughput-file",
        help="opt in to a best-effort, read-only sequential throughput probe of this existing file",
    )
    probe.add_argument(
        "--disk-throughput-bytes",
        type=int,
        help="maximum bytes to read (default from config/runtime/requirements.json)",
    )
    probe.set_defaults(func=command_probe)

    doctor = commands.add_parser("doctor", help="evaluate the target Pascal host requirements")
    doctor.add_argument("--output", help="also atomically write the snapshot and verdict")
    doctor.set_defaults(func=command_doctor)

    build = commands.add_parser("build", help="build and validate the pinned llama.cpp CUDA runtime")
    build.add_argument("--jobs", type=int, default=max(1, min(os.cpu_count() or 1, 12)))
    build.add_argument("--dry-run", action="store_true", help="print commands without executing or creating files")
    build.set_defaults(func=command_build)

    fetch = commands.add_parser("fetch", help="fetch exact artifacts from config/models.lock.json")
    fetch.add_argument("model_ids", metavar="MODEL_ID", nargs="*")
    fetch.add_argument("--lock", help="override model lock (intended for isolated testing)")
    fetch.add_argument("--all", action="store_true", help="fetch every locked artifact")
    fetch.add_argument("--list", action="store_true", help="list locked artifacts without network access")
    fetch.add_argument("--allow-unverified", action="store_true", help="dangerous: allow unresolved revision/SHA")
    fetch.add_argument("--dry-run", action="store_true", help="print URLs and targets without network or writes")
    fetch.set_defaults(func=command_fetch)

    sanity = commands.add_parser(
        "sanity",
        help="run deterministic Gated DeltaNet CPU/CUDA checks before any 27B execution",
    )
    sanity.add_argument("--lock", help="override model lock (intended for isolated testing)")
    sanity.add_argument(
        "--approve",
        action="store_true",
        help="write the explicit approval artifact only after both legs pass",
    )
    sanity.add_argument(
        "--dry-run",
        action="store_true",
        help="print both ephemeral server commands and the shared /completion request without execution",
    )
    sanity.set_defaults(func=command_sanity)

    serve = commands.add_parser("serve", help="run the locked baseline on a loopback address")
    serve.add_argument("model_id", metavar="MODEL_ID")
    serve.add_argument("--lock", help="override model lock (intended for isolated testing)")
    serve.add_argument("--port", type=int, help="override the loopback TCP port")
    serve.add_argument(
        "--optimization-profile",
        help="baseline, q4-kv, ngram or prompt-cache; experimental profiles need >=10%% measured speedup",
    )
    serve.add_argument("--dry-run", action="store_true", help="validate and print argv without launching")
    serve.set_defaults(func=command_serve)

    bench = commands.add_parser("bench", help="run the configured llama-bench matrix")
    bench.add_argument("model_id", metavar="MODEL_ID")
    bench.add_argument("--lock", help="override model lock (intended for isolated testing)")
    bench.add_argument("--case", type=int, help="run one zero-based matrix case")
    bench.add_argument("--max-cases", type=int, help="run only the first N selected cases")
    bench.add_argument("--confirm-full-matrix", action="store_true", help="permit more than 16 expensive cases")
    bench.add_argument("--dry-run", action="store_true", help="print the matrix without running the model")
    bench.set_defaults(func=command_bench)
    return root


def main(argv: Sequence[str] | None = None) -> int:
    try:
        args = parser().parse_args(argv)
        if getattr(args, "jobs", 1) < 1:
            raise LabError("--jobs must be positive")
        if getattr(args, "max_cases", None) is not None and args.max_cases < 1:
            raise LabError("--max-cases must be positive")
        if getattr(args, "disk_throughput_bytes", None) is not None and args.disk_throughput_bytes < 1:
            raise LabError("--disk-throughput-bytes must be positive")
        if getattr(args, "all", False) and getattr(args, "model_ids", []):
            raise LabError("do not combine --all with explicit MODEL_ID values")
        return int(args.func(args))
    except LabError as exc:
        print(f"qwen-lab: error: {exc}", file=sys.stderr)
        return 2
    except KeyboardInterrupt:
        print("qwen-lab: interrupted", file=sys.stderr)
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
