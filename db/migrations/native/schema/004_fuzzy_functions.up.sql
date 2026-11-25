-- Elasticsearch's fuzziness AUTO: no edit up to 2 letters, one up to 5, two above.
CREATE FUNCTION fuzzy_edit_budget(word text) RETURNS integer
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN CASE WHEN length(word) <= 2 THEN 0 WHEN length(word) <= 5 THEN 1 ELSE 2 END;

-- Edits as Elasticsearch counts them (optimal string alignment), a swap of two
-- neighbouring letters as one; past the budget it stops and returns budget + 1.
CREATE FUNCTION fuzzy_edit_distance(a text, b text, budget integer) RETURNS integer
    LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE
AS $$
DECLARE
    a_letters text[] := string_to_array(a, NULL);
    b_letters text[] := string_to_array(b, NULL);
    a_length integer := length(a);
    b_length integer := length(b);
    over integer := budget + 1;
    two_rows_up integer[];
    row_up integer[];
    this_row integer[];
    row_min integer;
    cell integer;
BEGIN
    IF a = b THEN
        RETURN 0;
    END IF;
    IF abs(a_length - b_length) > budget THEN
        RETURN over;
    END IF;
    two_rows_up := array_fill(over, ARRAY[b_length + 1], ARRAY[0]);
    row_up := array_fill(over, ARRAY[b_length + 1], ARRAY[0]);
    FOR j IN 0 .. least(b_length, budget) LOOP
        row_up[j] := j;
    END LOOP;
    FOR i IN 1 .. a_length LOOP
        this_row := array_fill(over, ARRAY[b_length + 1], ARRAY[0]);
        IF i <= budget THEN
            this_row[0] := i;
        END IF;
        row_min := this_row[0];
        FOR j IN greatest(1, i - budget) .. least(b_length, i + budget) LOOP
            cell := least(row_up[j] + 1, this_row[j - 1] + 1,
                          row_up[j - 1] + CASE WHEN a_letters[i] = b_letters[j] THEN 0 ELSE 1 END);
            IF i > 1 AND j > 1 AND a_letters[i] = b_letters[j - 1] AND a_letters[i - 1] = b_letters[j] THEN
                cell := least(cell, two_rows_up[j - 2] + 1);
            END IF;
            this_row[j] := least(cell, over);
            row_min := least(row_min, this_row[j]);
        END LOOP;
        IF row_min > budget THEN
            RETURN over;
        END IF;
        two_rows_up := row_up;
        row_up := this_row;
    END LOOP;
    RETURN row_up[b_length];
END;
$$;

-- Each query word's corpus words within its edit budget, fewest edits first, then most
-- frequent, at most 50 a word as Elasticsearch's max_expansions.
CREATE FUNCTION fuzzy_expansions(query text) RETURNS TABLE (query_position bigint, word text)
    LANGUAGE sql STABLE PARALLEL SAFE
BEGIN ATOMIC
    WITH typed AS MATERIALIZED (
        SELECT split.query_position, split.word, length(split.word) AS letters, fuzzy_edit_budget(split.word) AS budget,
               -- A swap saves an edit only where a corpus word holds one of these pairs.
               (SELECT string_agg(substr(split.word, i + 1, 1) || substr(split.word, i, 1), '|')
                FROM generate_series(1, length(split.word) - 1) AS i
                WHERE substr(split.word, i, 1) <> substr(split.word, i + 1, 1)) AS swapped_pairs
        -- unnest is estimated at 10 words, regexp_split_to_table at 1000, enough to JIT the scans.
        FROM unnest(regexp_split_to_array(lower(query), '[^a-z]+')) WITH ORDINALITY AS split (word, query_position)
        WHERE split.word <> ''
    )
    SELECT typed.query_position, near.word
    FROM typed
    CROSS JOIN LATERAL (
        SELECT w.word, w.ndoc,
               CASE WHEN gate.levenshtein <= 1 OR NOT coalesce(w.word ~ typed.swapped_pairs, false)
                    THEN gate.levenshtein
                    ELSE fuzzy_edit_distance(w.word, typed.word, typed.budget)
               END AS edits
        FROM corpus_words w
        -- Levenshtein counts a swap as two edits, so twice the budget passes every word within it.
        CROSS JOIN LATERAL (SELECT levenshtein_less_equal(w.word, typed.word, 2 * typed.budget) AS levenshtein) gate
        WHERE length(w.word) BETWEEN typed.letters - typed.budget AND typed.letters + typed.budget
          AND gate.levenshtein <= 2 * typed.budget
        ORDER BY edits, w.ndoc DESC, w.word
        LIMIT 50
    ) near
    WHERE near.edits <= typed.budget
    ORDER BY typed.query_position, near.edits, near.ndoc DESC, near.word;
END;

-- A word's expansions ORed, the words ANDed.
CREATE FUNCTION fuzzy_tsquery(query text) RETURNS tsquery
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN (
        SELECT to_tsquery('english', string_agg('(' || alternatives || ')', ' & ' ORDER BY query_position))
        FROM (
            SELECT query_position, string_agg(word, ' | ' ORDER BY word) AS alternatives
            FROM fuzzy_expansions(query)
            GROUP BY query_position
        ) per_word
    );
