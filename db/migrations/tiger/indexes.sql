SET pg_textsearch.segments_per_level = 8;
CREATE INDEX idx_articles_bm25 ON articles
    USING bm25 (search_text) WITH (text_config = 'english', k1 = 1.2, b = 0.75);
CREATE INDEX idx_articles_title_bm25 ON articles
    USING bm25 (title) WITH (text_config = 'english', k1 = 1.2, b = 0.75);
CREATE INDEX idx_article_embedding ON article_embeddings
    USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);
INSERT INTO corpus_words
    SELECT word, ndoc
    FROM ts_stat('SELECT to_tsvector(''simple'', title || '' '' || description || '' '' || content) FROM articles')
    WHERE word ~ '^[a-z]+$' AND length(word) <= 40;
ANALYZE corpus_words;
CREATE INDEX idx_corpus_words_trgm ON corpus_words USING gin (word gin_trgm_ops);
