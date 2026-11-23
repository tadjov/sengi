-- Verifies a cc-news canonical package against its own descriptor and an earlier corpus, recomputing
-- the preprocess rules apart from datapipe. Prints every check and exits 1 if any fails.
--
--   duckdb -bail -f scripts/verify_cc_news_canonical.sql
--
-- CANONICAL (default datasets/cc-news/canonical/v1) is the package to verify, PREVIOUS (default the
-- archived first-run corpus) the one it must agree with on every kept article.
-- The rules mirror datasets/cc-news/preprocess.yaml. Resource hashes are checked by datapipe.
.mode markdown
SET VARIABLE canonical = coalesce(nullif(getenv('CANONICAL'), ''), 'datasets/cc-news/canonical/v1');
SET VARIABLE previous = coalesce(nullif(getenv('PREVIOUS'), ''), '.archive/cc-news-v1/datasets/cc-news/canonical/v1');

-- Go's whitespace collapse: trimmed, each run of unicode.IsSpace runes as one space.
CREATE MACRO collapse(x) AS trim(regexp_replace(x, '[\t\n\x0B\f\r \x{85}\x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}]+', ' ', 'g'), ' ');

CREATE VIEW articles AS SELECT * FROM read_parquet(getvariable('canonical') || '/articles.parquet');
CREATE VIEW rejects AS SELECT * FROM read_parquet(getvariable('canonical') || '/rejects.parquet');
CREATE VIEW changes AS SELECT * FROM read_parquet(getvariable('canonical') || '/changes.parquet');
CREATE VIEW previous AS SELECT * FROM read_parquet(getvariable('previous') || '/articles.parquet');
CREATE VIEW descriptor AS SELECT content::JSON AS j FROM read_text(getvariable('canonical') || '/datapackage.json');

CREATE SEQUENCE check_order;
CREATE TEMP TABLE checks (position INTEGER DEFAULT nextval('check_order'), section VARCHAR, name VARCHAR, expected BIGINT, observed BIGINT);

-- Integrity

INSERT INTO checks (section, name, expected, observed)
SELECT 'integrity', 'distinct ids', count(*), count(DISTINCT id) FROM articles
UNION ALL SELECT 'integrity', 'distinct urls', count(*), count(DISTINCT url) FROM articles
UNION ALL SELECT 'integrity', 'articles missing an id, url or title', 0,
    count(*) FILTER (id IS NULL OR url IS NULL OR title IS NULL OR collapse(title) = '') FROM articles
UNION ALL SELECT 'integrity', 'optional values written as an empty string', 0,
    count(*) FILTER (description = '' OR content = '' OR publishedAt = '' OR metadata = '') FROM articles
UNION ALL SELECT 'integrity', 'articles in a language other than eng', 0, count(*) FILTER (language IS DISTINCT FROM 'eng') FROM articles;

INSERT INTO checks (section, name, expected, observed)
SELECT 'descriptor', 'records read = written + rejects', (SELECT (j->>'$.preprocess.counts.read')::BIGINT FROM descriptor),
    (SELECT count(*) FROM articles) + (SELECT count(*) FROM rejects)
UNION ALL SELECT 'descriptor', 'articles written', (SELECT (j->>'$.preprocess.counts.written')::BIGINT FROM descriptor), (SELECT count(*) FROM articles)
UNION ALL SELECT 'descriptor', 'dropped ' || d.key, d.value::BIGINT,
    (SELECT count(*) FROM rejects WHERE stage || '/' || coalesce(field, '') = d.key)
    FROM descriptor, json_each(j, '$.preprocess.counts.dropped') d
UNION ALL SELECT 'descriptor', 'duplicates ' || d.key, d.value::BIGINT,
    (SELECT count(*) FROM rejects WHERE stage = 'duplicates' AND rule = d.key)
    FROM descriptor, json_each(j, '$.preprocess.counts.duplicates') d
UNION ALL SELECT 'descriptor', 'cleared ' || d.key, d.value::BIGINT, (SELECT count(*) FROM changes WHERE rule = d.key)
    FROM descriptor, json_each(j, '$.preprocess.counts.cleared') d;

INSERT INTO checks (section, name, expected, observed)
SELECT 'rejects', 'duplicates without a kept twin in the corpus', 0,
    (SELECT count(*) FROM rejects r ANTI JOIN articles a ON a.id = r.keptId WHERE r.stage = 'duplicates')
UNION ALL SELECT 'rejects', 'rejected ids still in the corpus', 0, (SELECT count(*) FROM rejects r SEMI JOIN articles a ON a.id = r.id)
UNION ALL SELECT 'changes', 'cleared fields not NULL in the corpus', 0,
    (SELECT count(*) FROM changes c JOIN articles a USING (id)
     WHERE (c.field = 'Content' AND a.content IS NOT NULL) OR (c.field = 'Description' AND a.description IS NOT NULL))
UNION ALL SELECT 'changes', 'changes naming an article not in the corpus', 0, (SELECT count(*) FROM changes c ANTI JOIN articles a USING (id));

-- Rules, recomputed from the values before clearing: per site, the distinct headlines sharing one body or description.

CREATE TEMP TABLE original AS
SELECT a.id, a.publisherDomain AS domain, md5(collapse(a.title)) AS title_key,
       md5(nullif(collapse(coalesce(body.value, a.content)), '')) AS content_key,
       md5(nullif(collapse(coalesce(lead.value, a.description)), '')) AS description_key,
       body.id IS NOT NULL AS body_cleared, body.countDistinct AS body_count,
       lead.id IS NOT NULL AS description_cleared, lead.countDistinct AS description_count
FROM articles a
LEFT JOIN changes body ON body.id = a.id AND body.field = 'Content'
LEFT JOIN changes lead ON lead.id = a.id AND lead.field = 'Description';

CREATE TEMP TABLE grouped AS
SELECT 'shared-body' AS rule, 3 AS at_least, o.id, g.headlines, o.body_cleared AS cleared, o.body_count AS recorded
FROM original o
JOIN (SELECT domain, content_key, count(DISTINCT title_key) AS headlines FROM original
      WHERE nullif(domain, '') IS NOT NULL AND content_key IS NOT NULL GROUP BY ALL) g USING (domain, content_key)
UNION ALL
SELECT 'shared-description', 5, o.id, g.headlines, o.description_cleared, o.description_count
FROM original o
JOIN (SELECT domain, description_key, count(DISTINCT title_key) AS headlines FROM original
      WHERE nullif(domain, '') IS NOT NULL AND description_key IS NOT NULL GROUP BY ALL) g USING (domain, description_key);

INSERT INTO checks (section, name, expected, observed)
SELECT 'rules', rule || ': cleared', count(*) FILTER (headlines >= at_least), count(*) FILTER (cleared) FROM grouped GROUP BY rule, at_least
UNION ALL SELECT 'rules', rule || ': at or over the threshold, not cleared', 0, count(*) FILTER (headlines >= at_least AND NOT cleared) FROM grouped GROUP BY rule
UNION ALL SELECT 'rules', rule || ': under the threshold, cleared', 0, count(*) FILTER (headlines < at_least AND cleared) FROM grouped GROUP BY rule
UNION ALL SELECT 'rules', rule || ': recorded count differs', 0, count(*) FILTER (cleared AND recorded <> headlines) FROM grouped GROUP BY rule
UNION ALL SELECT 'rules', r.key || ': articles in a group', (r.value->>'articles')::BIGINT, (SELECT count(*) FROM grouped g WHERE g.rule = r.key)
    FROM descriptor, json_each(j, '$.preprocess.rules') r
UNION ALL SELECT 'rules', r.key || ': largest group', (r.value->>'max')::BIGINT, (SELECT max(headlines) FROM grouped g WHERE g.rule = r.key)
    FROM descriptor, json_each(j, '$.preprocess.rules') r
UNION ALL SELECT 'rules', r.key || ': at least ' || t.key, t.value::BIGINT,
    (SELECT count(*) FROM grouped g WHERE g.rule = r.key AND g.headlines >= t.key::INT)
    FROM descriptor, json_each(j, '$.preprocess.rules') r, json_each(r.value, '$.atLeast') t;

-- Against the earlier corpus: nothing new, every missing article a duplicate, every kept one unchanged
-- apart from cleared fields and an empty string now written as NULL.

CREATE TEMP TABLE compared AS
SELECT a.id, a.url, a.title, a.publishedAt, a.language, a.publisherDomain, a.metadata, a.content, a.description,
       p.url AS p_url, p.title AS p_title, p.publishedAt AS p_published, p.language AS p_language,
       p.publisherDomain AS p_domain, p.metadata AS p_metadata, p.content AS p_content, p.description AS p_description,
       body.value AS cleared_content, lead.value AS cleared_description
FROM articles a JOIN previous p USING (id)
LEFT JOIN changes body ON body.id = a.id AND body.field = 'Content'
LEFT JOIN changes lead ON lead.id = a.id AND lead.field = 'Description';

CREATE MACRO same_or_emptied(now, before) AS now IS NOT DISTINCT FROM before OR (now IS NULL AND before = '');

INSERT INTO checks (section, name, expected, observed)
SELECT 'previous', 'articles not in the previous corpus', 0, (SELECT count(*) FROM articles ANTI JOIN previous USING (id))
UNION ALL SELECT 'previous', 'previous articles gone without a duplicate reject', 0,
    (SELECT count(*) FROM (SELECT url FROM previous ANTI JOIN articles USING (id)) gone
     ANTI JOIN rejects r ON r.url = gone.url AND r.stage = 'duplicates')
UNION ALL SELECT 'previous', 'same-page duplicates whose kept twin is another page', 0,
    (SELECT count(*) FROM rejects r
     JOIN previous dropped ON dropped.url = r.url
     JOIN previous kept ON kept.id = r.keptId
     WHERE r.rule = 'same-page'
       AND (dropped.publisherDomain IS DISTINCT FROM kept.publisherDomain
            OR collapse(dropped.title) IS DISTINCT FROM collapse(kept.title)
            OR collapse(dropped.content) IS DISTINCT FROM collapse(kept.content)))
UNION ALL SELECT 'previous', 'url, title, language, domain or metadata changed', 0,
    count(*) FILTER (url IS DISTINCT FROM p_url OR title IS DISTINCT FROM p_title OR language IS DISTINCT FROM p_language
                     OR publisherDomain IS DISTINCT FROM p_domain OR metadata IS DISTINCT FROM p_metadata) FROM compared
UNION ALL SELECT 'previous', 'publishedAt changed', 0, count(*) FILTER (NOT same_or_emptied(publishedAt, p_published)) FROM compared
UNION ALL SELECT 'previous', 'content changed', 0,
    count(*) FILTER (CASE WHEN cleared_content IS NOT NULL THEN cleared_content IS DISTINCT FROM p_content
                          ELSE NOT same_or_emptied(content, p_content) END) FROM compared
UNION ALL SELECT 'previous', 'description changed', 0,
    count(*) FILTER (CASE WHEN cleared_description IS NOT NULL THEN cleared_description IS DISTINCT FROM p_description
                          ELSE NOT same_or_emptied(description, p_description) END) FROM compared;

SELECT section, name, expected, observed, CASE WHEN expected IS NOT DISTINCT FROM observed THEN 'ok' ELSE 'FAIL' END AS result
FROM checks ORDER BY position;

SELECT count(*) FILTER (content IS NOT NULL) AS with_body,
       count(*) FILTER (content IS NULL AND description IS NOT NULL) AS title_and_description,
       count(*) FILTER (content IS NULL AND description IS NULL) AS title_only,
       count(*) FILTER (length(content) < 200) AS body_under_200_chars,
       median(length(content))::BIGINT AS median_body_chars,
       count(*) FILTER (publishedAt IS NULL) AS no_date
FROM articles;

SELECT error(count(*) || ' of ' || (SELECT count(*) FROM checks) || ' checks failed') FROM checks
WHERE expected IS DISTINCT FROM observed HAVING count(*) > 0;
