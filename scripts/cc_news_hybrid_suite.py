import json
from pathlib import Path

OUT = Path(__file__).resolve().parent.parent / "tracks/cc-news/semantic-hybrid/suite.yaml"

# (id, description, text)
KEYWORD = [
    ("kw-catalonia-referendum", "Articles about the Catalan independence referendum", "catalonia independence referendum"),
    ("kw-rohingya-refugees", "Articles about Rohingya refugees", "rohingya refugees"),
    ("kw-tax-reform-bill", "Articles about the Republican tax bill", "republican tax bill"),
    ("kw-parkland-shooting", "Articles about the Parkland school shooting", "parkland school shooting"),
    ("kw-royal-wedding", "Articles about the royal wedding of Prince Harry and Meghan Markle", "royal wedding"),
    ("kw-world-cup-russia", "Articles about the 2018 World Cup in Russia", "world cup russia"),
    ("kw-trump-kim-summit", "Articles about the summit between Donald Trump and Kim Jong Un", "trump kim summit"),
    ("kw-amazon-hq2", "Articles about Amazon's search for a second headquarters", "amazon second headquarters"),
    ("kw-hawaii-missile-alert", "Articles about the false missile alert in Hawaii", "hawaii missile alert"),
    ("kw-weinstein-allegations", "Articles about the allegations against Harvey Weinstein", "harvey weinstein allegations"),
]
QUESTION = [
    ("q-thai-cave-boys", "Articles about the rescue of the boys trapped in a Thai cave", "how were the boys trapped in the thai cave rescued"),
    ("q-bees-dying", "Articles about why honeybees are dying", "why are honeybees dying"),
    ("q-tariffs-farmers", "Articles about how tariffs hurt American farmers", "how do tariffs hurt american farmers"),
    ("q-government-shutdown", "Articles about why the US government shut down", "why did the us government shut down"),
    ("q-vaping-teens", "Articles about the dangers of vaping for teenagers", "is vaping dangerous for teenagers"),
    ("q-venezuela-economy", "Articles about the collapse of Venezuela's economy", "why is venezuela's economy collapsing"),
    ("q-electric-cars", "Articles about whether electric cars can replace petrol cars", "can electric cars replace petrol cars"),
    ("q-toys-r-us", "Articles about why Toys R Us went out of business", "why did toys r us go out of business"),
    ("q-irish-border", "Articles about how Brexit affects the Irish border", "how will brexit affect the irish border"),
    ("q-crispr-embryos", "Articles about the safety of gene editing human embryos", "is gene editing of human embryos safe"),
]
DESCRIPTIVE = [
    ("desc-automation", "Articles about automation replacing human jobs", "machines taking over work that people used to do"),
    ("desc-cord-cutting", "Articles about cord-cutting: viewers leaving cable for streaming", "people cancelling their cable subscriptions for online streaming"),
    ("desc-flint-water", "Articles about the Flint water crisis", "a city whose drinking water was contaminated with lead from old pipes"),
    ("desc-tech-taxes-europe", "Articles about US tech companies' taxes in Europe", "big american technology companies paying too little tax in europe"),
    ("desc-plastic-oceans", "Articles about plastic pollution in the oceans", "plastic waste ending up in the sea and harming marine life"),
    ("desc-arctic-ice", "Articles about the melting of Arctic sea ice", "ice melting at the north pole"),
    ("desc-fake-news", "Articles about fake news and disinformation aimed at voters", "false stories spread online to influence voters"),
    ("desc-gig-economy", "Articles about the gig economy", "workers paid per task through apps without employee benefits"),
    ("desc-superbugs", "Articles about antibiotic resistance", "bacteria that medicines no longer kill"),
    ("desc-wildfire-evacuations", "Articles about wildfire evacuations", "forest fires forcing thousands to flee their homes"),
]

# Every leg orders its own ties by id, and the SQL fusions rank inside each leg
# the same way, so pg-rrf and the client-side pg-rrf-client fuse the same input.
# Vector legs break ties before the LIMIT, and HNSW still serves the distance.
TEMPLATES = """templates:
  - id: pg_multi_field
    args: [terms, rank_norm, limit]
    query: |
      SELECT id FROM articles
      WHERE search_vector @@ plainto_tsquery('english', $1)
      ORDER BY ts_rank(search_vector, plainto_tsquery('english', $1), $2::int) DESC, id
      LIMIT $3::int

  - id: pdb_multi_field_boosted
    args: [terms, limit]
    query: |
      SELECT id FROM articles
      WHERE id @@@ paradedb.boolean(
        should => ARRAY[
          paradedb.boost(3.0, paradedb.match('title', $1)),
          paradedb.boost(2.0, paradedb.match('description', $1)),
          paradedb.match('content', $1)
        ]
      )
      ORDER BY pdb.score(id) DESC, id
      LIMIT $2::int

  - id: tiger_multi_field
    args: [terms, limit]
    query: |
      WITH matched AS MATERIALIZED (
        SELECT id, search_text FROM articles WHERE search_text @@ plainto_tsquery('english', $1)
      )
      SELECT id FROM matched
      ORDER BY search_text <@> to_bm25query($1, 'idx_articles_bm25'), id
      LIMIT $2::int

  - id: es_multi_match_boosted
    args: [terms]
    query: |
      {"query": {"multi_match": {"query": "{{terms}}", "fields": ["title^3", "description^2", "content"]}}, "size": 100, "sort": ["_score", {"id": "asc"}], "_source": false, "track_total_hits": false}

  - id: pg_nearest
    args: [embedding_model, query_vector, limit]
    query: |
      SELECT id FROM (
        SELECT article_id AS id, embedding <=> $2::vector AS distance
        FROM article_embeddings
        WHERE model_name = $1
        ORDER BY embedding <=> $2::vector, article_id
        LIMIT $3::int
      ) nearest
      ORDER BY distance, id

  # k above size lets the id sort cut tied vectors, not load order.
  - id: es_knn
    args: [query_vector]
    query: |
      {"knn": {"field": "embedding", "query_vector": {{#toJson}}query_vector{{/toJson}}, "k": 200, "num_candidates": 200}, "size": 100, "sort": ["_score", {"id": "asc"}], "_source": false, "track_total_hits": false}

  # Elasticsearch's own one-request hybrid on the basic licence: the BM25 and kNN
  # scores are summed with the default boosts.
  # Tied vectors are cut by load order; no sort reaches the kNN cut.
  - id: es_linear
    args: [terms, query_vector]
    query: |
      {"query": {"multi_match": {"query": "{{terms}}", "fields": ["title^3", "description^2", "content"]}}, "knn": {"field": "embedding", "query_vector": {{#toJson}}query_vector{{/toJson}}, "k": 100, "num_candidates": 200}, "size": 100, "sort": ["_score", {"id": "asc"}], "_source": false, "track_total_hits": false}

  - id: pg_rrf
    args: [terms, rank_norm, embedding_model, query_vector, window, rank_constant]
    query: |
      WITH lexical AS (
        SELECT id, ROW_NUMBER() OVER (ORDER BY score DESC, id) AS rank FROM (
          SELECT id, ts_rank(search_vector, plainto_tsquery('english', $1), $2::int) AS score
          FROM articles
          WHERE search_vector @@ plainto_tsquery('english', $1)
          ORDER BY score DESC, id
          LIMIT $5::int
        ) top
      ),
      vector AS (
        SELECT id, ROW_NUMBER() OVER (ORDER BY distance, id) AS rank FROM (
          SELECT article_id AS id, embedding <=> $4::vector AS distance
          FROM article_embeddings
          WHERE model_name = $3
          ORDER BY embedding <=> $4::vector, article_id
          LIMIT $5::int
        ) nearest
      )
      SELECT id FROM (
        SELECT COALESCE(l.id, v.id) AS id,
               COALESCE(1.0::float8 / ($6::int + l.rank), 0) + COALESCE(1.0::float8 / ($6::int + v.rank), 0) AS score
        FROM lexical l FULL OUTER JOIN vector v ON l.id = v.id
      ) fused
      ORDER BY score DESC, id
      LIMIT $5::int

  - id: pdb_rrf
    args: [terms, embedding_model, query_vector, window, rank_constant]
    query: |
      WITH lexical AS (
        SELECT id, ROW_NUMBER() OVER (ORDER BY score DESC, id) AS rank FROM (
          SELECT id, pdb.score(id) AS score
          FROM articles
          WHERE id @@@ paradedb.boolean(
            should => ARRAY[
              paradedb.boost(3.0, paradedb.match('title', $1)),
              paradedb.boost(2.0, paradedb.match('description', $1)),
              paradedb.match('content', $1)
            ]
          )
          ORDER BY pdb.score(id) DESC, id
          LIMIT $4::int
        ) top
      ),
      vector AS (
        SELECT id, ROW_NUMBER() OVER (ORDER BY distance, id) AS rank FROM (
          SELECT article_id AS id, embedding <=> $3::vector AS distance
          FROM article_embeddings
          WHERE model_name = $2
          ORDER BY embedding <=> $3::vector, article_id
          LIMIT $4::int
        ) nearest
      )
      SELECT id FROM (
        SELECT COALESCE(l.id, v.id) AS id,
               COALESCE(1.0::float8 / ($5::int + l.rank), 0) + COALESCE(1.0::float8 / ($5::int + v.rank), 0) AS score
        FROM lexical l FULL OUTER JOIN vector v ON l.id = v.id
      ) fused
      ORDER BY score DESC, id
      LIMIT $4::int

  - id: tiger_rrf
    args: [terms, embedding_model, query_vector, window, rank_constant]
    query: |
      WITH matched AS MATERIALIZED (
        SELECT id, search_text FROM articles WHERE search_text @@ plainto_tsquery('english', $1)
      ),
      lexical AS (
        SELECT id, ROW_NUMBER() OVER (ORDER BY score, id) AS rank FROM (
          SELECT id, search_text <@> to_bm25query($1, 'idx_articles_bm25') AS score
          FROM matched
          ORDER BY score, id
          LIMIT $4::int
        ) top
      ),
      vector AS (
        SELECT id, ROW_NUMBER() OVER (ORDER BY distance, id) AS rank FROM (
          SELECT article_id AS id, embedding <=> $3::vector AS distance
          FROM article_embeddings
          WHERE model_name = $2
          ORDER BY embedding <=> $3::vector, article_id
          LIMIT $4::int
        ) nearest
      )
      SELECT id FROM (
        SELECT COALESCE(l.id, v.id) AS id,
               COALESCE(1.0::float8 / ($5::int + l.rank), 0) + COALESCE(1.0::float8 / ($5::int + v.rank), 0) AS score
        FROM lexical l FULL OUTER JOIN vector v ON l.id = v.id
      ) fused
      ORDER BY score DESC, id
      LIMIT $4::int
"""

HEADER = """schema_version: 1
id: cc_news_semantic_hybrid_v1
name: "cc-news semantic and hybrid v1"
description: "Keyword, vector and hybrid search over cc-news: 30 natural-language queries, 10 keyword, 10 question, 10 descriptive"
version: "1.0.0"

corpus:
  name: cc-news-canonical@v1
  source: "sha256:7cdf4c719dc70cd013a1eb76fa33688564dc99af03ddf8cc57d20952cd4c89a3 (datasets/cc-news/canonical/v1/datapackage.json)"

"""


def template(name, params):
    lines = [f"      {name}:", f"        template: {TEMPLATE_OF[name]}"]
    if params:
        rendered = ", ".join(f"{key}: {json.dumps(value)}" for key, value in params.items())
        lines.append(f"        params: {{ {rendered} }}")
    return lines


TEMPLATE_OF = {
    "pg-gin": "pg_multi_field",
    "paradedb": "pdb_multi_field_boosted",
    "tiger": "tiger_multi_field",
    "elasticsearch": "es_multi_match_boosted",
    "pg-hnsw": "pg_nearest",
    "paradedb-hnsw": "pg_nearest",
    "tiger-hnsw": "pg_nearest",
    "es-knn": "es_knn",
    "es-linear": "es_linear",
    "pg-rrf": "pg_rrf",
    "paradedb-rrf": "pdb_rrf",
    "tiger-rrf": "tiger_rrf",
}
LIMITED = {"pg-gin", "paradedb", "tiger", "pg-hnsw", "paradedb-hnsw", "tiger-hnsw"}
TAKES_TERMS = {"pg-gin", "paradedb", "tiger", "elasticsearch", "es-linear", "pg-rrf", "paradedb-rrf", "tiger-rrf"}


def block(qid, category, description, text):
    lines = [f"  - id: {qid}", f"    text: {json.dumps(text)}", f"    description: {json.dumps(description)}",
             f"    category: {category}", "    engines:"]
    for engine in TEMPLATE_OF:
        params = {}
        if engine in TAKES_TERMS:
            params["terms"] = text
        if engine in LIMITED:
            params["limit"] = 100
        lines += template(engine, params)
    return "\n".join(lines)


blocks = [block(q, "keyword", d, t) for q, d, t in KEYWORD]
blocks += [block(q, "question", d, t) for q, d, t in QUESTION]
blocks += [block(q, "descriptive", d, t) for q, d, t in DESCRIPTIVE]

OUT.parent.mkdir(parents=True, exist_ok=True)
with open(OUT, "w") as out:
    out.write(HEADER + TEMPLATES + "\nqueries:\n" + "\n\n".join(blocks) + "\n")
print(len(blocks), "queries written to", OUT)
