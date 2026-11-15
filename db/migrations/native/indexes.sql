ALTER TABLE articles ADD CONSTRAINT articles_pkey PRIMARY KEY (id);
ALTER TABLE article_embeddings ADD CONSTRAINT article_embeddings_pkey PRIMARY KEY (article_id, model_name);
ALTER TABLE article_embeddings ADD CONSTRAINT article_embeddings_article_id_fkey
    FOREIGN KEY (article_id) REFERENCES articles (id) ON DELETE CASCADE;
CREATE INDEX idx_articles_published_at ON articles (published_at DESC NULLS LAST);
CREATE INDEX idx_articles_publisher_domain ON articles (publisher_domain);
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
