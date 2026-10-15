-- «Поделиться» (ADR-73): ссылка на версию факта, только чтение, хозяин отзывает. В базе — хэш ссылки.
CREATE TABLE shares (
    id          UUID PRIMARY KEY,
    token_hash  TEXT NOT NULL UNIQUE,
    owner_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    fact_id     UUID NOT NULL REFERENCES facts(id),
    version     INT  NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at  TIMESTAMPTZ
);
CREATE INDEX shares_fact ON shares (fact_id);
