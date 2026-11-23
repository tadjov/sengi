# CC-News

HuggingFace [vblagoje/cc_news](https://huggingface.co/datasets/vblagoje/cc_news), the
CommonCrawl News crawl with bodies extracted by
[news-please](https://github.com/fhamborg/news-please).

|          |                                                                               |
|----------|-------------------------------------------------------------------------------|
| Articles | 708,241                                                                       |
| Domains  | 8,759                                                                         |
| Period   | 2017-01 to 2019-08                                                            |
| Language | English                                                                       |
| Columns  | `title`, `text`, `description`, `url`, `date`, `domain` (+ unused `image_url`) |
| On HF    | 5 parquet shards, ~1.1 GB                                                     |

77,598 rows (11%) carry an empty `date`. The fetch script writes those as an empty
field, which `datapipe preprocess` skips since the target is optional. `domain` is a
hostname such as `www.pointemagazine.com` and is not mapped: preprocess derives
`publisherDomain` from the url instead (`pointemagazine.com`), the same way for every
corpus.

```
datasets/cc-news/
  raw/                       mirrored HuggingFace shards (--mirror)
  dataset-100k.csv           slice written by scripts/fetch_hf_dataset.py
  dataset-100k.meta.json     provenance: repo, mode, seed, row count
  canonical/<version>/       datapipe preprocess output, a canonical data package
  embeddings.parquet         Colab embeddings (qwen3-embedding:0.6b)
```

Payloads are gitignored, so fetch or regenerate them.

## Fetch

```bash
uv run scripts/fetch_hf_dataset.py cc_news --mirror           # shards to raw/, once
uv run scripts/fetch_hf_dataset.py cc_news                    # whole corpus, 708,241 rows
uv run scripts/fetch_hf_dataset.py cc_news 100000             # reservoir sample of 100k
uv run scripts/fetch_hf_dataset.py cc_news 100000 500000      # nested slices from one scan
```

A row count above 708,241 writes the whole corpus as `dataset-full.csv` instead of
failing, so the file name always states what is in it.

Mirror first if you plan to cut more than one slice. Sampling against HuggingFace streams
all 1.1 GB and took 482 s here, against 130 s to mirror the same bytes and 1.4 s per
sample afterwards. The results match: the 20k sample cut from the mirror agreed with the
one cut over the network on all 20,000 URLs.

Sampling is the default because the parquet is stored in crawl order. The first 20k rows
cover 273 of the 8,759 domains, a 20k sample covers 3,641, and that skew would land in
the IDF and BM25 numbers. `--mode head` takes the first N rows instead, which is only
worth it for a quick look before mirroring.

Two runs with the same `--seed` drew the identical sample here, and slices are cut in
`hash(url)` order so a smaller size is a prefix of a larger one. DuckDB promises neither
across versions, so treat the written file plus its `.meta.json` as the archived
artifact. `--format parquet` writes the same columns. Slices are not data packages, so
`datapipe preprocess` reads the mirrored `raw/` package, not a slice.

## Preprocess and load

```bash
go run ./cmd/datapipe preprocess datasets/cc-news/preprocess.yaml --version v1

ENV_PATHS=cmd/datapipe/pg.env go run ./cmd/datapipe load datasets/cc-news/canonical/<version>/datapackage.json \
  --engine native \
  --embeddings datasets/cc-news/embeddings/<tag-dir>/<version>/datapackage.json
```

`mapping.yaml` is the source's shape and `preprocess.yaml` our policy. Of 708,241 raw records, v1 drops 331 without a title and 41,665 copies of a page on the same site (same title and body), and keeps 666,245 articles. It then clears 43,599 bodies that three or more different headlines share on one site and 18,680 descriptions that five or more share: cookie banners, taglines, or one story's body scraped under unrelated headlines. 8,178 articles keep only their title. The package's `rejects.parquet` and `changes.parquet` list every record removed and every field cleared.

The thresholds come from the data. From three headlines up, 85-91% of shared-body groups have unrelated headlines (mean word Jaccard under 0.2), while at two only half do. Shared descriptions are site taglines from five headlines up, and at three or four they still include real stories republished under edited headlines.
