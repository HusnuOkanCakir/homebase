from __future__ import annotations

import sys
import unittest
from pathlib import Path


LAB_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(LAB_ROOT))

from tools.retrieve import Chunk, chunk_document, retrieve  # noqa: E402


class RetrieverTests(unittest.TestCase):
    def test_chunks_are_capped_and_retrieval_is_deterministic(self) -> None:
        document = {
            "document_id": "long",
            "title": "Long",
            "language": "en",
            "text": " ".join(["neutral"] * 520 + ["orchid"] * 20),
        }
        chunks = chunk_document(document)
        self.assertEqual([512, 28], [chunk.token_count for chunk in chunks])
        first = retrieve("orchid", chunks, top_k=2)
        second = retrieve("orchid", chunks, top_k=2)
        self.assertEqual(first, second)
        self.assertEqual("long-0001", first[0]["chunk_id"])
        self.assertTrue(all(item["token_count"] <= 512 for item in first))

    def test_top_k_is_hard_capped_at_four(self) -> None:
        chunks = [
            Chunk(str(index), str(index), "t", "en", "query", 1, str(index))
            for index in range(5)
        ]
        with self.assertRaises(ValueError):
            retrieve("query", chunks, top_k=5)


if __name__ == "__main__":
    unittest.main()
