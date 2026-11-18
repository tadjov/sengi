CREATE INDEX idx_articles_search_vector ON articles USING gin (search_vector);
CREATE INDEX idx_articles_title_trgm ON articles USING gin (title gin_trgm_ops);
CREATE INDEX idx_articles_title_desc_trgm ON articles
    USING gin ((title || ' ' || coalesce(description, '')) gin_trgm_ops);
CREATE INDEX idx_article_embedding ON article_embeddings
    USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);
INSERT INTO corpus_words
    SELECT word, ndoc
    FROM ts_stat('SELECT to_tsvector(''simple'', title || '' '' || description || '' '' || content) FROM articles')
    WHERE word ~ '^[a-z]+$' AND length(word) <= 40;
ANALYZE corpus_words;
CREATE INDEX idx_corpus_words_trgm ON corpus_words USING gin (word gin_trgm_ops);
