#!/usr/bin/env python3
"""Small deterministic BM25 retriever for offline Qwen lab evaluations."""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import re
import sys
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable


TOKEN_RE = re.compile(r"\w+|[^\w\s]", re.UNICODE)
DEFAULT_CHUNK_TOKENS = 512
MAX_TOP_K = 4


def tokenize(text: str) -> list[str]:
    """Return stable, Unicode-aware approximate tokens."""
    return [token.casefold() for token in TOKEN_RE.findall(text)]


@dataclass(frozen=True)
class Chunk:
    document_id: str
    chunk_id: str
    title: str
    language: str
    text: str
    token_count: int
    sha256: str


def chunk_document(document: dict, max_tokens: int = DEFAULT_CHUNK_TOKENS) -> list[Chunk]:
    if not 1 <= max_tokens <= DEFAULT_CHUNK_TOKENS:
        raise ValueError(f"max_tokens must be between 1 and {DEFAULT_CHUNK_TOKENS}")
    document_id = str(document["document_id"])
    raw_tokens = TOKEN_RE.findall(str(document["text"]))
    chunks: list[Chunk] = []
    for index, start in enumerate(range(0, len(raw_tokens), max_tokens)):
        part = raw_tokens[start : start + max_tokens]
        text = " ".join(part)
        chunks.append(
            Chunk(
                document_id=document_id,
                chunk_id=f"{document_id}-{index:04d}",
                title=str(document.get("title", document_id)),
                language=str(document.get("language", "und")),
                text=text,
                token_count=len(part),
                sha256=hashlib.sha256(text.encode("utf-8")).hexdigest(),
            )
        )
    return chunks


def load_corpus(path: Path, max_tokens: int = DEFAULT_CHUNK_TOKENS) -> list[Chunk]:
    chunks: list[Chunk] = []
    with path.open(encoding="utf-8") as handle:
        for line_number, line in enumerate(handle, 1):
            if not line.strip():
                continue
            try:
                document = json.loads(line)
                chunks.extend(chunk_document(document, max_tokens=max_tokens))
            except (KeyError, TypeError, ValueError, json.JSONDecodeError) as exc:
                raise ValueError(f"invalid corpus line {line_number}: {exc}") from exc
    return chunks


def retrieve(query: str, chunks: Iterable[Chunk], top_k: int = MAX_TOP_K) -> list[dict]:
    """Rank chunks with BM25 and return serializable citations."""
    if not 1 <= top_k <= MAX_TOP_K:
        raise ValueError(f"top_k must be between 1 and {MAX_TOP_K}")
    materialized = list(chunks)
    if not materialized:
        return []
    query_terms = tokenize(query)
    document_terms = [tokenize(chunk.text) for chunk in materialized]
    average_length = sum(map(len, document_terms)) / len(document_terms)
    document_frequency = {
        term: sum(term in set(tokens) for tokens in document_terms) for term in set(query_terms)
    }
    k1, b = 1.5, 0.75
    ranked: list[tuple[float, Chunk]] = []
    for chunk, terms in zip(materialized, document_terms):
        score = 0.0
        length_normalizer = k1 * (1 - b + b * len(terms) / max(average_length, 1.0))
        for term in query_terms:
            frequency = terms.count(term)
            if not frequency:
                continue
            frequency_docs = document_frequency[term]
            inverse_frequency = math.log(1 + (len(materialized) - frequency_docs + 0.5) / (frequency_docs + 0.5))
            score += inverse_frequency * frequency * (k1 + 1) / (frequency + length_normalizer)
        ranked.append((score, chunk))
    ranked.sort(key=lambda item: (-item[0], item[1].document_id, item[1].chunk_id))
    output = []
    positive_ranked = [item for item in ranked if item[0] > 0]
    for score, chunk in positive_ranked[:top_k]:
        output.append(
            {
                "document_id": chunk.document_id,
                "chunk_id": chunk.chunk_id,
                "title": chunk.title,
                "language": chunk.language,
                "text": chunk.text,
                "token_count": chunk.token_count,
                "sha256": chunk.sha256,
                "score": round(score, 8),
                "citation": f"[source:{chunk.document_id}#{chunk.chunk_id}]",
            }
        )
    return output


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--corpus", type=Path, required=True)
    parser.add_argument("--query", required=True)
    parser.add_argument("--top-k", type=int, default=MAX_TOP_K)
    parser.add_argument("--chunk-tokens", type=int, default=DEFAULT_CHUNK_TOKENS)
    parser.add_argument("--output", type=Path, help="Write JSON here; stdout is used when omitted.")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    payload = {
        "schema_version": "qwen38-retrieval/v1",
        "query": args.query,
        "top_k": args.top_k,
        "chunks": retrieve(
            args.query,
            load_corpus(args.corpus, max_tokens=args.chunk_tokens),
            top_k=args.top_k,
        ),
    }
    serialized = json.dumps(payload, ensure_ascii=False, indent=2) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        temporary = args.output.with_name(
            f".{args.output.name}.{os.getpid()}.{uuid.uuid4().hex}.tmp"
        )
        try:
            temporary.write_text(serialized, encoding="utf-8")
            os.replace(temporary, args.output)
        finally:
            try:
                temporary.unlink()
            except FileNotFoundError:
                pass
    else:
        sys.stdout.write(serialized)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
