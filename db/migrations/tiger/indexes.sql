SET pg_textsearch.segments_per_level = 8;
CREATE INDEX idx_articles_bm25 ON articles
    USING bm25 (search_text) WITH (text_config = 'english', k1 = 1.2, b = 0.75);
CREATE INDEX idx_articles_title_bm25 ON articles
    USING bm25 (title) WITH (text_config = 'english', k1 = 1.2, b = 0.75);
CREATE INDEX idx_article_embedding ON article_embeddings
    USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);
