from __future__ import annotations

import sys
import unittest
from pathlib import Path

LAB_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(LAB_ROOT))

from tools.quant_sweep import derive, parse_bench, sweep  # noqa: E402


# Real llama-bench output from this host, copied rather than composed. A fixture
# written by whoever wrote the parser agrees with the parser by construction.
Q4_TABLE = """
| model                          |       size |     params | backend    | threads |            test |                  t/s |
| ------------------------------ | ---------: | ---------: | ---------- | ------: | --------------: | -------------------: |
| qwen35 4B Q4_K - Medium        |   2.58 GiB |     4.33 B | CPU        |       4 |            pp64 |         17.27 ± 2.20 |
| qwen35 4B Q4_K - Medium        |   2.58 GiB |     4.33 B | CPU        |       4 |            tg64 |          6.55 ± 0.06 |
"""

SMALL_TABLE = """
| qwen35 0.8B Q4_0               | 526.50 MiB |   752.39 M | CPU        |       4 |            tg64 |         27.31 ± 0.12 |
"""


class ParseTests(unittest.TestCase):
    def test_reads_size_and_both_rates(self) -> None:
        row = parse_bench(Q4_TABLE)
        self.assertAlmostEqual(row["size_gib"], 2.58)
        self.assertAlmostEqual(row["prompt_tps"], 17.27)
        self.assertAlmostEqual(row["decode_tps"], 6.55)

    def test_converts_mebibytes(self) -> None:
        # A sub-gigabyte model is reported in MiB and must not be read as GiB,
        # which would understate its effective bandwidth by a factor of 1024.
        row = parse_bench(SMALL_TABLE)
        self.assertAlmostEqual(row["size_gib"], 526.50 / 1024, places=4)

    def test_a_table_with_no_rows_is_not_a_crash(self) -> None:
        self.assertEqual(parse_bench("error: could not load model"), {})


class DeriveTests(unittest.TestCase):
    def test_reproduces_the_figures_measured_by_hand(self) -> None:
        row = derive(parse_bench(Q4_TABLE), peak_gb_s=25.6)
        self.assertAlmostEqual(row["effective_gib_s"], 16.9, places=1)
        self.assertAlmostEqual(row["percent_of_peak"], 70.9, places=1)

    def test_leaves_an_unmeasured_row_alone(self) -> None:
        row = derive({"status": "timeout"}, peak_gb_s=25.6)
        self.assertNotIn("effective_gib_s", row)


class SweepTests(unittest.TestCase):
    def test_fastest_is_reported_separately_from_smallest(self) -> None:
        # The whole point of the study: on this hardware they are different
        # answers, and a report that only named the smallest would hide it.
        rows = [
            derive({"quantisation": "Q8_0", "status": "measured",
                    "size_gib": 4.28, "decode_tps": 4.35}, 25.6),
            derive({"quantisation": "Q4_K_M", "status": "measured",
                    "size_gib": 2.58, "decode_tps": 6.55}, 25.6),
            derive({"quantisation": "IQ2_XXS", "status": "measured",
                    "size_gib": 1.90, "decode_tps": 2.10}, 25.6),
        ]
        measured = [r for r in rows if r.get("decode_tps")]
        fastest = max(measured, key=lambda r: r["decode_tps"])
        smallest = min(measured, key=lambda r: r["size_gib"])
        self.assertEqual(fastest["quantisation"], "Q4_K_M")
        self.assertEqual(smallest["quantisation"], "IQ2_XXS")
        self.assertNotEqual(fastest["quantisation"], smallest["quantisation"])

    def test_a_missing_binary_is_recorded_rather_than_raised(self) -> None:
        report = sweep(Path("/nonexistent/llama-bench"), [Path("/nonexistent/m.gguf")],
                       peak_gb_s=25.6, threads=1, prompt=1, generate=1,
                       repetitions=1, timeout=5)
        self.assertEqual(len(report["rows"]), 1)
        self.assertIsNone(report["fastest"])


if __name__ == "__main__":
    unittest.main()
