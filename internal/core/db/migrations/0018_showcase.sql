-- Витрина (ADR-74): админ кладёт свои опубликованные факты, указывает язык и порядок; отдаётся открыто.
CREATE TABLE showcase (
    id          UUID PRIMARY KEY,
    fact_id     UUID NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
    language    TEXT NOT NULL CHECK (language ~ '^[a-z]{2}$'),
    position    INT  NOT NULL DEFAULT 0,
    created_by  UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (fact_id, language)
);
CREATE INDEX showcase_language ON showcase (language, position);
