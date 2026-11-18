"""Holm-adjusted Wilcoxon results for nDCG@10 and MAP from two run reports, side by side."""

import json
import sys
from pathlib import Path

METRICS = ("NDCG@10", "MAP")


def significance(path: Path) -> dict[tuple[str, str, str, str], dict]:
    report = json.loads(path.read_text())
    return {
        (job["JobName"], row["EngineA"], row["EngineB"], row["Metric"]): row
        for job in report["jobs"]
        for row in job["Significance"]
        if row["Metric"] in METRICS
    }


def main() -> None:
    first, second = Path(sys.argv[1]), Path(sys.argv[2])
    a, b = significance(first), significance(second)
    print(f"A = {first.name}\nB = {second.name}\n")
    print(f"{'job':<13} {'pair':<30} {'metric':<8} {'A mean diff':>11} {'A adj p':>8} {'B mean diff':>11} {'B adj p':>8}")
    for key in sorted(a):
        job, engine_a, engine_b, metric = key
        ra, rb = a[key], b[key]
        print(f"{job:<13} {engine_a + ' vs ' + engine_b:<30} {metric:<8} "
              f"{ra['MeanDifference']['Mean']:>11.4f} {ra['AdjustedP']:>8.3f} "
              f"{rb['MeanDifference']['Mean']:>11.4f} {rb['AdjustedP']:>8.3f}")


if __name__ == "__main__":
    main()
