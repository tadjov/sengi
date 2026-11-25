#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = [
#   "numpy>=1.26",
#   "pyarrow>=16",
# ]
# ///
"""Compare a `datapipe embed --truncate-to` package with its source, row by row and by search results.

  uv run experiments/embedding-width/scripts/compare_widths.py \
    <source embeddings.parquet> <cut embeddings.parquet> \
    <source query-vectors.parquet> <cut query-vectors.parquet> --out experiments/embedding-width/out

Writes summary.txt, per-query.tsv and manifest.json. Search is exact: every query against every article.
"""

import argparse
import hashlib
import json
from pathlib import Path
from typing import NamedTuple

import numpy as np
import pyarrow.compute as pc
import pyarrow.parquet as pq

DEPTHS = (10, 100)


class Vectors(NamedTuple):
    keys: list[str]
    texts: list[str]
    matrix: np.ndarray


class QueryOverlap(NamedTuple):
    query_id: str
    text: str
    shared: dict[int, int]
    top10_cosines: tuple[float, float]


def read_vectors(path: Path) -> Vectors:
    table = pq.read_table(path)
    column = table.column("embedding").combine_chunks()
    width = len(column[0].as_py())
    matrix = pc.list_flatten(column).to_numpy(zero_copy_only=False).astype(np.float32).reshape(len(column), width)
    key = "query_id" if "query_id" in table.column_names else "id"
    texts = table.column("text").to_pylist() if "text" in table.column_names else []
    return Vectors(keys=table.column(key).to_pylist(), texts=texts, matrix=matrix)


def top_k(scores: np.ndarray, depth: int) -> np.ndarray:
    candidates = np.argpartition(-scores, depth)[:depth]
    return candidates[np.lexsort((candidates, -scores[candidates]))]


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as file:
        for block in iter(lambda: file.read(1 << 20), b""):
            digest.update(block)
    return "sha256:" + digest.hexdigest()


def compare_rows(source: Vectors, cut: Vectors) -> list[str]:
    width = cut.matrix.shape[1]
    head = source.matrix[:, :width]
    expected = head / np.linalg.norm(head, axis=1, keepdims=True)
    norms = np.linalg.norm(cut.matrix, axis=1)
    return [
        f"articles: {len(source.keys):,} at {source.matrix.shape[1]} dims, {len(cut.keys):,} at {width}",
        f"same ids in the same order: {source.keys == cut.keys}",
        f"largest |cut - source's first {width} dims renormalised|: {np.abs(cut.matrix - expected).max():.2e}",
        f"norms: {norms.min():.7f} to {norms.max():.7f}",
        f"non-finite values: {int((~np.isfinite(cut.matrix)).sum())}",
    ]


def compare_search(source: Vectors, cut: Vectors, source_queries: Vectors, cut_queries: Vectors) -> list[QueryOverlap]:
    position = {query_id: i for i, query_id in enumerate(source_queries.keys)}
    overlaps = []
    for i, query_id in enumerate(cut_queries.keys):
        full_scores = source.matrix @ source_queries.matrix[position[query_id]]
        cut_scores = cut.matrix @ cut_queries.matrix[i]
        shared = {depth: len(set(top_k(full_scores, depth)) & set(top_k(cut_scores, depth))) for depth in DEPTHS}
        top10 = full_scores[top_k(full_scores, 10)]
        overlaps.append(
            QueryOverlap(
                query_id=query_id,
                text=cut_queries.texts[i],
                shared=shared,
                top10_cosines=(float(top10.max()), float(top10.min())),
            )
        )
    return overlaps


def summarise(overlaps: list[QueryOverlap]) -> list[str]:
    lines = []
    for depth in DEPTHS:
        shares = np.array([overlap.shared[depth] / depth for overlap in overlaps])
        lines.append(
            f"top-{depth} shared by both widths over {len(overlaps)} queries: "
            f"mean {shares.mean():.3f}, median {np.median(shares):.3f}, min {shares.min():.3f}"
        )
    return lines


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("source", type=Path)
    parser.add_argument("cut", type=Path)
    parser.add_argument("source_queries", type=Path)
    parser.add_argument("cut_queries", type=Path)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()

    source, cut = read_vectors(args.source), read_vectors(args.cut)
    source_queries, cut_queries = read_vectors(args.source_queries), read_vectors(args.cut_queries)
    overlaps = compare_search(source, cut, source_queries, cut_queries)

    args.out.mkdir(parents=True, exist_ok=True)
    summary = args.out / "summary.txt"
    summary.write_text("\n".join(compare_rows(source, cut) + summarise(overlaps)) + "\n")
    per_query = args.out / "per-query.tsv"
    rows = ["query_id\tshared_top10\tshared_top100\ttop10_cosine_max\ttop10_cosine_min\ttext"]
    for overlap in sorted(overlaps, key=lambda o: (o.shared[10], o.query_id)):
        cosine_max, cosine_min = overlap.top10_cosines
        rows.append(
            f"{overlap.query_id}\t{overlap.shared[10]}\t{overlap.shared[100]}\t{cosine_max:.4f}\t{cosine_min:.4f}\t{overlap.text}"
        )
    per_query.write_text("\n".join(rows) + "\n")

    inputs = {
        "source": args.source,
        "cut": args.cut,
        "source_queries": args.source_queries,
        "cut_queries": args.cut_queries,
        "script": Path(__file__).resolve().relative_to(Path.cwd().resolve()),
    }
    manifest = {
        "inputs": {role: {"path": str(path), "sha256": sha256(path)} for role, path in inputs.items()},
        "outputs": {path.name: sha256(path) for path in (summary, per_query)},
    }
    (args.out / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(summary.read_text(), end="")


if __name__ == "__main__":
    main()
