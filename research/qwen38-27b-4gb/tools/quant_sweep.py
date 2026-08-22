#!/usr/bin/env python3
"""Locate the quantisation optimum on a bandwidth-limited CPU.

The lab was designed around fitting weights into 4 GiB of VRAM, which pushes
towards ever fewer bits. Measurement on this host says that is the wrong
objective for CPU inference, because the curve is not monotonic:

    Q8_0     4.28 GiB   4.35 tok/s   18.6 GiB/s   78% of peak
    Q4_K_M   2.58 GiB   6.55 tok/s   16.9 GiB/s   71% of peak
    IQ2_XXS  6.76 GiB   0.61 tok/s    4.1 GiB/s   17% of peak

Between 4 and 8 bits fewer bytes wins, because the work is dominated by moving
weights. Below that the unpacking costs more than the fetch and the gain goes
into arithmetic instead. There is an optimum in between and this tool finds it.

Everything is derived from two measured numbers per run — file size and decode
rate — so the output can be checked by hand:

    effective GiB/s   = size_gib * decode_tokens_per_second
    percent of peak   = effective / theoretical_peak

The theoretical peak must be supplied: it is a property of the machine's memory
controller, not something llama-bench can report.
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

try:
    from .evaluate import atomic_write_text
except ImportError:  # Direct execution: python tools/quant_sweep.py
    from evaluate import atomic_write_text

GIB = 1024 ** 3

# llama-bench prints a markdown table; this pulls the size and the two rates.
_ROW = re.compile(
    r"\|\s*(?P<model>[^|]+?)\s*\|\s*(?P<size>[\d.]+)\s*(?P<unit>[GM]iB)\s*\|"
    r"[^|]*\|[^|]*\|[^|]*\|\s*(?P<test>\w+)\s*\|\s*(?P<tps>[\d.]+)"
)


def parse_bench(output: str) -> dict[str, Any]:
    """Pull size and rates out of a llama-bench table.

    Returns what was found rather than raising: a case that produced no rows is
    a result too, and the caller records it as such.
    """
    found: dict[str, Any] = {}
    for match in _ROW.finditer(output):
        size = float(match.group("size"))
        if match.group("unit") == "MiB":
            size /= 1024
        found["size_gib"] = size
        found["model"] = match.group("model").strip()
        test = match.group("test")
        rate = float(match.group("tps"))
        if test.startswith("pp"):
            found["prompt_tps"] = rate
        elif test.startswith("tg"):
            found["decode_tps"] = rate
    return found


def derive(row: dict[str, Any], peak_gb_s: float) -> dict[str, Any]:
    """Add the two derived figures the whole study turns on."""
    size = row.get("size_gib")
    decode = row.get("decode_tps")
    if not size or not decode:
        return row
    effective = size * decode
    row["effective_gib_s"] = round(effective, 2)
    # GiB/s to GB/s before comparing against a peak quoted in GB/s.
    row["percent_of_peak"] = round(effective * 1.073741824 / peak_gb_s * 100, 1)
    return row


def run_case(binary: Path, model: Path, threads: int, prompt: int,
             generate: int, repetitions: int, timeout: int) -> dict[str, Any]:
    argv = [str(binary), "-m", str(model), "-t", str(threads),
            "-p", str(prompt), "-n", str(generate), "-r", str(repetitions)]
    try:
        completed = subprocess.run(
            argv, capture_output=True, text=True, timeout=timeout,
            stdin=subprocess.DEVNULL, check=False)
    except subprocess.TimeoutExpired:
        return {"status": "timeout", "seconds": timeout}
    except OSError as error:
        # A missing binary or an unreadable model is a result, not a crash. A
        # sweep that dies on its first bad path loses every case after it.
        return {"status": "unavailable", "error": str(error)}
    if completed.returncode != 0:
        return {"status": "failed", "exit_code": completed.returncode,
                "stderr_tail": completed.stderr.strip().splitlines()[-3:]}
    row = parse_bench(completed.stdout + completed.stderr)
    if "decode_tps" not in row:
        return {"status": "unparsed", "stdout_tail": completed.stdout.strip().splitlines()[-3:]}
    row["status"] = "measured"
    row["file_bytes"] = model.stat().st_size
    return row


def sweep(binary: Path, models: list[Path], peak_gb_s: float, threads: int,
          prompt: int, generate: int, repetitions: int,
          timeout: int) -> dict[str, Any]:
    rows = []
    for model in models:
        row = {"path": str(model), "quantisation": model.stem.split("-")[-1]}
        row.update(run_case(binary, model, threads, prompt, generate,
                            repetitions, timeout))
        rows.append(derive(row, peak_gb_s))

    measured = [r for r in rows if r.get("status") == "measured" and r.get("decode_tps")]
    best = max(measured, key=lambda r: r["decode_tps"], default=None)
    return {
        "schema_version": 1,
        "captured_at": datetime.now(timezone.utc).isoformat(),
        "settings": {"threads": threads, "prompt_tokens": prompt,
                     "generation_tokens": generate, "repetitions": repetitions,
                     "theoretical_peak_gb_s": peak_gb_s},
        "rows": rows,
        "fastest": None if best is None else {
            "quantisation": best["quantisation"],
            "decode_tps": best["decode_tps"],
            "size_gib": best["size_gib"],
        },
        # Stated rather than implied: the point of the sweep is that the answer
        # is not simply "the smallest file".
        "smallest": None if not measured else min(
            measured, key=lambda r: r["size_gib"])["quantisation"],
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bench", type=Path, required=True,
                        help="path to llama-bench")
    parser.add_argument("--peak-gb-s", type=float, required=True,
                        help="the machine's theoretical memory bandwidth, in GB/s")
    parser.add_argument("--threads", type=int, default=4)
    parser.add_argument("--prompt", type=int, default=64)
    parser.add_argument("--generate", type=int, default=64)
    parser.add_argument("--repetitions", type=int, default=2)
    parser.add_argument("--timeout", type=int, default=1800)
    parser.add_argument("--out", type=Path)
    parser.add_argument("models", type=Path, nargs="+")
    args = parser.parse_args(argv)

    report = sweep(args.bench, args.models, args.peak_gb_s, args.threads,
                   args.prompt, args.generate, args.repetitions, args.timeout)
    text = json.dumps(report, indent=2, ensure_ascii=False) + "\n"
    if args.out:
        atomic_write_text(args.out, text)
    print(text, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
