# cross-judge

How the cc-news fts relevance judgments agree across judges, and whether the engine ranking holds when the judge changes. Judges: `claude-sonnet-5-5` (primary, full pool), `jev-1.14.0` (TypeSafe, full pool and the 436-document sample), `claude-opus-5-5` and `claude-haiku-5-5` (sample). Every number comes from `bench diff judgments` (bench at `c10f64d`), except the significance tests and the relevant-document counts, which come from the scripts here.

## Outputs

| file | what |
|---|---|
| `out/sample.txt`, `out/sample.json` | agreement of the four judges on the sample, Opus as the reference |
| `out/sample-vs-sonnet.txt`, `out/sample-vs-sonnet.json` | the same, sonnet as the reference |
| `out/self-agreement.txt`, `out/self-agreement.json` | sonnet's run-1 judgments against its run-2 judgments, 3,681 shared documents |
| `out/rescore.txt`, `out/rescore.json` | sonnet against Jev on the full pool, and the latest run report rescored under both |
| `out/significance.txt` | Holm-adjusted Wilcoxon results for nDCG@10 and MAP, sonnet-scored run against the Jev-scored run |
| `out/relevant-per-query.txt` | relevant documents per query, against the ranking depth of 50 AP sees |
| `out/manifest.json` | sha256 of every input and output, bench commit, bootstrap seed |

The sample uses the sample's own Jev run (`cross-judge/annotations.jev.yaml`); the full-pool Jev file differs from it on 9 documents, since Jev is not deterministic. `bench diff judgments --report` rescores rankings but runs no significance test, so Jev's tests come from run `2026-11-14T23-56-57-run-b2a175`: the same 300 rankings as the latest run, scored with Jev's judgments.

## Reproduce

From the repository root, with `bench` built at `c10f64d` or later:

```sh
export BENCH_TRACK_ROOT=$PWD/tracks/cc-news
T=tracks/cc-news/fts/trec
E=experiments/cross-judge
SAMPLE=sonnet=claude-cli,opus=$T/cross-judge/annotations.opus.yaml,jev=$T/cross-judge/annotations.jev.yaml,haiku=$T/cross-judge/annotations.haiku.yaml
git show 9cf1794:tracks/cc-news/fts/trec/annotations.claude-cli.yaml > /tmp/sonnet-run1.yaml

bench diff judgments fts --judgments $SAMPLE --reference opus --output $E/out/sample.json > $E/out/sample.txt
bench diff judgments fts --judgments $SAMPLE --reference sonnet --output $E/out/sample-vs-sonnet.json > $E/out/sample-vs-sonnet.txt
bench diff judgments fts --judgments run1=/tmp/sonnet-run1.yaml,run2=claude-cli --output $E/out/self-agreement.json > $E/out/self-agreement.txt
bench diff judgments fts --judgments sonnet=claude-cli,jev=jev --reference sonnet --report latest --output $E/out/rescore.json > $E/out/rescore.txt
python3 $E/scripts/significance.py tracks/cc-news/fts/reports/2026-11-14T20-26-29-run-183a31.json tracks/cc-news/fts/reports/2026-11-14T23-56-57-run-b2a175.json > $E/out/significance.txt
uv run --script $E/scripts/relevant_per_query.py $T/annotations.claude-cli.yaml $T/annotations.jev.yaml > $E/out/relevant-per-query.txt
```

The bootstrap seed is 1 (the default), so the confidence intervals reproduce byte for byte.
