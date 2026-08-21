#!/usr/bin/env python3
"""Dependency-free structural checks for the isolated Qwen lab."""

from __future__ import annotations

import json
from pathlib import Path
import re
import sys


ROOT = Path(__file__).resolve().parent
REVISION_RE = re.compile(r"^[0-9a-f]{40}$")
SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
SKIP_PARTS = {"__pycache__", ".ipynb_checkpoints"}
TEXT_SUFFIXES = {".json", ".jsonl", ".md", ".py", ".sh", ".ipynb"}
REQUIRED_MODEL_FIELDS = {
    "id",
    "repository",
    "filename",
    "revision",
    "sha256",
    "size_bytes",
    "role",
    "license",
    "provenance",
}
REQUIRED_PATHS = {
    "README.md",
    "bin/qwen-lab",
    "bin/self-test",
    "config/models.lock.json",
    "config/runtime/bench-matrix.json",
    "config/runtime/llama-cpp.lock.json",
    "eval/fixtures/cases.jsonl",
    "lib/qwen_lab.py",
    "notebooks/colab_zero_budget_probe.ipynb",
    "tools/acceptance.py",
    "tools/compare_optimization.py",
    "tools/evaluate.py",
    "tools/retrieve.py",
}


def files(suffix: str) -> list[Path]:
    return sorted(
        path
        for path in ROOT.rglob(f"*{suffix}")
        if not any(part in SKIP_PARTS for part in path.relative_to(ROOT).parts)
    )


def check_text(errors: list[str]) -> None:
    for path in sorted(path for path in ROOT.rglob("*") if path.is_file()):
        if any(part in SKIP_PARTS for part in path.relative_to(ROOT).parts):
            continue
        if path.suffix not in TEXT_SUFFIXES and path.name not in {"Makefile", ".gitignore"}:
            continue
        try:
            text = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            errors.append(f"{path}: is not UTF-8")
            continue
        if text and not text.endswith("\n"):
            errors.append(f"{path}: missing final newline")
        for number, line in enumerate(text.splitlines(), 1):
            if line.rstrip() != line:
                errors.append(f"{path}:{number}: trailing whitespace")


def check_json(errors: list[str]) -> None:
    for path in [*files(".json"), *files(".ipynb")]:
        try:
            document = json.loads(path.read_text(encoding="utf-8"))
        except json.JSONDecodeError as exc:
            errors.append(f"{path}: invalid JSON: {exc}")
            continue
        if path.suffix == ".ipynb":
            for index, cell in enumerate(document.get("cells", [])):
                if cell.get("outputs"):
                    errors.append(f"{path}: cell {index} contains committed output")
                if cell.get("cell_type") == "code":
                    source = "".join(cell.get("source", []))
                    try:
                        compile(source, f"{path}:cell-{index}", "exec")
                    except SyntaxError as exc:
                        errors.append(f"{path}: cell {index} does not compile: {exc}")
    for path in files(".jsonl"):
        for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            if not line:
                continue
            try:
                value = json.loads(line)
            except json.JSONDecodeError as exc:
                errors.append(f"{path}:{number}: invalid JSONL: {exc}")
                continue
            if not isinstance(value, dict):
                errors.append(f"{path}:{number}: JSONL record must be an object")


def check_model_lock(errors: list[str]) -> None:
    path = ROOT / "config" / "models.lock.json"
    document = json.loads(path.read_text(encoding="utf-8"))
    if document.get("schema_version") != 1:
        errors.append(f"{path}: expected schema_version 1")
    ids: set[str] = set()
    destinations: set[str] = set()
    for index, model in enumerate(document.get("models", [])):
        missing = REQUIRED_MODEL_FIELDS - model.keys()
        if missing:
            errors.append(f"{path}: models[{index}] missing {sorted(missing)}")
        model_id = str(model.get("id", ""))
        if model_id in ids:
            errors.append(f"{path}: duplicate model id {model_id}")
        ids.add(model_id)
        destination = re.sub(r"[^A-Za-z0-9._-]+", "_", model_id).strip("._")
        if destination in destinations:
            errors.append(f"{path}: model ids collide after path normalization: {model_id}")
        destinations.add(destination)
        if not REVISION_RE.fullmatch(str(model.get("revision", ""))):
            errors.append(f"{path}: {model_id} revision is not a full immutable SHA")
        if not SHA256_RE.fullmatch(str(model.get("sha256", ""))):
            errors.append(f"{path}: {model_id} SHA-256 is unresolved")
        size = model.get("size_bytes")
        if isinstance(size, bool) or not isinstance(size, int) or size <= 0:
            errors.append(f"{path}: {model_id} has invalid size_bytes")


def check_runtime_lock(errors: list[str]) -> None:
    path = ROOT / "config" / "runtime" / "llama-cpp.lock.json"
    lock = json.loads(path.read_text(encoding="utf-8"))
    expected = {
        "build": "b10549",
        "revision": "b2e5e9b28b2484fbf94b543432ece638996a8b97",
    }
    for key, value in expected.items():
        if lock.get(key) != value:
            errors.append(f"{path}: expected {key}={value}")
    if lock.get("cuda", {}).get("required_release") != "12.9":
        errors.append(f"{path}: CUDA release must remain 12.9 for Pascal")
    if lock.get("cuda", {}).get("architectures") != ["60", "61"]:
        errors.append(f"{path}: expected Pascal architectures 60 and 61")


def main() -> int:
    errors: list[str] = []
    for relative in sorted(REQUIRED_PATHS):
        if not (ROOT / relative).is_file():
            errors.append(f"missing required lab file: {relative}")
    check_text(errors)
    check_json(errors)
    check_model_lock(errors)
    check_runtime_lock(errors)
    if errors:
        print("Qwen lab validation failed:", file=sys.stderr)
        for error in errors:
            print(f"  - {error}", file=sys.stderr)
        return 1
    print("Qwen lab: structure, locks, JSON/JSONL and notebook cells are valid.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
