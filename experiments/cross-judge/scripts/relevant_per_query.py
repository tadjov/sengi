# /// script
# requires-python = ">=3.12"
# dependencies = ["pyyaml"]
# ///
"""Relevant documents per query in each judgment set, against the ranking depth AP sees."""

import sys
from collections import Counter
from pathlib import Path

import yaml

THRESHOLD = 2
RANKING_DEPTH = 50


def relevant_per_query(path: Path) -> Counter:
    data = yaml.load(path.read_text(), Loader=yaml.BaseLoader)
    return Counter({q["query_id"]: sum(int(d["grade"]) >= THRESHOLD for d in q["docs"]) for q in data["queries"]})


def main() -> None:
    for path in map(Path, sys.argv[1:]):
        counts = relevant_per_query(path)
        over = sum(n > RANKING_DEPTH for n in counts.values())
        values = sorted(counts.values())
        print(f"{path.name}: {over} of {len(counts)} queries have more than {RANKING_DEPTH} relevant documents "
              f"(median {values[len(values) // 2]}, min {values[0]}, max {values[-1]})")


if __name__ == "__main__":
    main()
