# embedding-width

What storing 512 dimensions instead of 1024 changes on cc-news. Qwen3-Embedding-0.6B is Matryoshka-trained, so `datapipe embed --truncate-to 512` keeps each vector's first 512 dimensions and renormalises them. The experiment checks that the cut package is exactly that, and how many of the exact nearest articles the cut keeps for the 30 semantic-hybrid queries, searched against all 666,245 articles with no index.

## Outputs

| file | what |
|---|---|
| `out/summary.txt` | the row checks, and the top-10 and top-100 overlap over the 30 queries |
| `out/per-query.tsv` | per query: articles shared in the top 10 and top 100, and the cosine range of the 1024 top 10 |
| `out/manifest.json` | sha256 of every input, the script and both outputs |

Overlap is a stability measure, not a quality one: an article that enters the 512 top 10 is not shown to be worse than the one it replaced. The query ids carry their kind (`kw-` keyword, `q-` question, `desc-` description).

## Reproduce

From the repository root, with the two embeddings packages in `datasets/cc-news/embeddings/` (`@1024/v1` from the Kaggle run, `@512/v1` cut from it):

```sh
git show ec673c6:tracks/cc-news/semantic-hybrid/trec/query-vectors/query-vectors.parquet > /tmp/qv-1024.parquet
uv run experiments/embedding-width/scripts/compare_widths.py \
  datasets/cc-news/embeddings/qwen3-embedding-0.6b@1024/v1/embeddings.parquet \
  datasets/cc-news/embeddings/qwen3-embedding-0.6b@512/v1/embeddings.parquet \
  /tmp/qv-1024.parquet \
  tracks/cc-news/semantic-hybrid/trec/query-vectors/query-vectors.parquet \
  --out experiments/embedding-width/out
```

`ec673c6` is the last commit with the 1024 query vectors, before #106 (store and search vectors at 512 dims) cut them. The search is exact and has no randomness, so the outputs reproduce byte for byte.
