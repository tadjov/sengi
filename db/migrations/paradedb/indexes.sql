ALTER TABLE articles ADD CONSTRAINT articles_pkey PRIMARY KEY (id);
ALTER TABLE article_embeddings ADD CONSTRAINT article_embeddings_pkey PRIMARY KEY (article_id, model_name);
ALTER TABLE article_embeddings ADD CONSTRAINT article_embeddings_article_id_fkey
    FOREIGN KEY (article_id) REFERENCES articles (id) ON DELETE CASCADE;
CREATE INDEX idx_articles_published_at ON articles (published_at DESC NULLS LAST);
CREATE INDEX idx_articles_search ON articles USING bm25 (
    id,
    (title::pdb.simple('stemmer=english', 'stopwords_language=english')),
    (description::pdb.simple('stemmer=english', 'stopwords_language=english')),
    (content::pdb.simple('stemmer=english', 'stopwords_language=english')),
    published_at,
    (publisher_domain::pdb.literal)
) WITH (key_field = 'id', target_segment_count = 1, mutable_segment_rows = 1000);
CREATE INDEX idx_article_embedding ON article_embeddings
    USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);
