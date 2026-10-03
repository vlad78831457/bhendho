-- Ядро OFFGRID: схема M0/M1/M2 (спека, раздел 7; ADR-43..48).
-- Деньги — NUMERIC(15,4); журналы append-only; все статусы — справочники CHECK.

CREATE TABLE currencies (
    id         TEXT PRIMARY KEY,                     -- стабильный id, не меняется никогда (ADR-27)
    kind       TEXT NOT NULL CHECK (kind IN ('internal', 'external')),
    code       TEXT NOT NULL,
    symbol     TEXT NOT NULL,
    is_active  BOOLEAN NOT NULL DEFAULT TRUE
);
INSERT INTO currencies (id, kind, code, symbol) VALUES ('GC', 'internal', 'GC', 'GC');

-- Пользователи и сессии (ADR-44).
CREATE TABLE users (
    id            UUID PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    display_name  TEXT NOT NULL DEFAULT '',
    priority      INT  NOT NULL DEFAULT 0,           -- приоритет между пользователями (ADR-36)
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE refresh_tokens (
    id          UUID PRIMARY KEY,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  TEXT NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_user ON refresh_tokens (user_id);

-- Кошельки: владелец × внутренняя валюта (ADR-27); резерв — ADR-29.
CREATE TABLE wallets (
    id          UUID PRIMARY KEY,
    owner_kind  TEXT NOT NULL CHECK (owner_kind IN ('user', 'service')),
    owner_id    UUID NOT NULL,
    currency_id TEXT NOT NULL REFERENCES currencies(id),
    balance     NUMERIC(15,4) NOT NULL DEFAULT 0 CHECK (balance >= 0),
    reserved    NUMERIC(15,4) NOT NULL DEFAULT 0 CHECK (reserved >= 0),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (reserved <= balance),
    UNIQUE (owner_kind, owner_id, currency_id)
);

-- Журнал денег — только INSERT (ADR-06).
CREATE TABLE transactions (
    id              BIGSERIAL PRIMARY KEY,
    wallet_id       UUID NOT NULL REFERENCES wallets(id),
    type            TEXT NOT NULL CHECK (type IN ('deposit', 'reserve', 'charge', 'release', 'income', 'refund', 'adjust')),
    amount          NUMERIC(15,4) NOT NULL CHECK (amount >= 0),
    counterparty_id UUID,                             -- «от кого → кому» (ADR-27)
    task_id         UUID,
    deposit_id      UUID,
    units           JSONB,                            -- счёт воркера в его единицах (ADR-47)
    rates           JSONB,                            -- курсы, зафиксированные в задаче
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX transactions_wallet ON transactions (wallet_id, id);
CREATE INDEX transactions_task ON transactions (task_id) WHERE task_id IS NOT NULL;

CREATE FUNCTION forbid_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME;
END $$;
CREATE TRIGGER transactions_append_only BEFORE UPDATE OR DELETE ON transactions
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- Депозиты и события провайдеров (ADR-26, ADR-31).
CREATE TABLE deposits (
    id               UUID PRIMARY KEY,
    user_id          UUID NOT NULL REFERENCES users(id),
    provider         TEXT NOT NULL,
    status           TEXT NOT NULL CHECK (status IN ('created', 'awaiting_payment', 'paid', 'failed', 'expired')),
    amount_fiat      NUMERIC(15,4) NOT NULL,
    fiat_currency    TEXT NOT NULL,
    amount_internal  NUMERIC(15,4) NOT NULL CHECK (amount_internal > 0),
    currency_id      TEXT NOT NULL REFERENCES currencies(id),
    rate             NUMERIC(15,6) NOT NULL,
    meta             JSONB NOT NULL DEFAULT '{}',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    paid_at          TIMESTAMPTZ
);

CREATE TABLE payment_events (
    id          BIGSERIAL PRIMARY KEY,
    deposit_id  UUID NOT NULL REFERENCES deposits(id),
    event       TEXT NOT NULL CHECK (event IN ('DEPOSIT_PAID', 'DEPOSIT_FAILED')),
    payload     JSONB NOT NULL DEFAULT '{}',
    dedup_key   TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Сервисы (стороны исполнения) и их прорабы (ADR-18, ADR-30, ADR-47).
CREATE TABLE services (
    id               UUID PRIMARY KEY,
    code             TEXT NOT NULL UNIQUE,            -- target_service
    module_id        TEXT NOT NULL,
    module_version   TEXT NOT NULL,
    title            TEXT NOT NULL,
    currency_id      TEXT NOT NULL REFERENCES currencies(id),
    accounting       JSONB,                           -- base_unit + extra_units из манифеста
    target_wallet_id UUID NOT NULL REFERENCES wallets(id),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE service_accounts (
    id          UUID PRIMARY KEY,
    service_id  UUID NOT NULL REFERENCES services(id),
    name        TEXT NOT NULL,
    token_hash  TEXT NOT NULL UNIQUE,
    revoked_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (service_id, name)
);

-- Шаблоны форм: версии append-only (ADR-19); kind = факт-форма или задача-форма (ADR-21).
CREATE TABLE forms (
    form_id     TEXT NOT NULL,
    version     INT  NOT NULL CHECK (version >= 1),
    kind        TEXT NOT NULL CHECK (kind IN ('fact', 'task')),
    service_id  UUID REFERENCES services(id),
    title       TEXT NOT NULL,
    meta_ui     JSONB NOT NULL,                       -- title, fields_rules, …
    price       NUMERIC(15,4) NOT NULL DEFAULT 0 CHECK (price >= 0),
    currency_id TEXT NOT NULL REFERENCES currencies(id),
    status      TEXT NOT NULL CHECK (status IN ('draft', 'published', 'withdrawn')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (form_id, version),
    CHECK (kind = 'fact' OR service_id IS NOT NULL)
);
CREATE INDEX forms_published ON forms (form_id, version DESC) WHERE status = 'published';

-- Факты: id_db + версии (ADR-13/15).
CREATE TABLE facts (
    id          UUID PRIMARY KEY,                     -- id_db
    user_id     UUID NOT NULL REFERENCES users(id),
    kind        TEXT NOT NULL,                        -- form_id факт-формы
    origin      TEXT NOT NULL DEFAULT 'user',         -- user | task:<id>
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX facts_user_kind ON facts (user_id, kind);

CREATE TABLE fact_versions (
    fact_id        UUID NOT NULL REFERENCES facts(id),
    version        INT  NOT NULL CHECK (version >= 1),
    schema_version INT  NOT NULL,
    status         TEXT NOT NULL CHECK (status IN ('draft', 'published', 'archived', 'deleted')),
    values         JSONB NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (fact_id, version)
);
CREATE INDEX fact_versions_published ON fact_versions (fact_id, version DESC) WHERE status = 'published';

-- Реестр файлов — единственное место правды о «живости» (ADR-16).
CREATE TABLE files (
    id           UUID PRIMARY KEY,
    user_id      UUID NOT NULL REFERENCES users(id),
    bucket       TEXT NOT NULL,
    object_uuid  UUID NOT NULL,
    filename     TEXT NOT NULL,
    size_bytes   BIGINT NOT NULL CHECK (size_bytes >= 0),
    mime         TEXT NOT NULL,
    parent_kind  TEXT CHECK (parent_kind IN ('fact', 'task', 'draft')),
    parent_id    UUID,
    field_code   TEXT,
    status       TEXT NOT NULL CHECK (status IN ('active', 'deleted')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at   TIMESTAMPTZ,
    UNIQUE (bucket, object_uuid)
);

-- Задачи (ADR-14/19/20/22/29/30/36/47).
CREATE TABLE system_tasks (
    id              UUID PRIMARY KEY,
    user_id         UUID NOT NULL REFERENCES users(id),
    form_id         TEXT NOT NULL,
    schema_version  INT  NOT NULL,
    target_service  TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('draft', 'template', 'pending', 'processing', 'completed', 'failed', 'cancelled')),
    values          JSONB NOT NULL,
    materialized    JSONB,                            -- снимок для воркера (ADR-14)
    wallet_id       UUID NOT NULL REFERENCES wallets(id),
    reserved        NUMERIC(15,4) NOT NULL CHECK (reserved >= 0),
    rates_fixed     JSONB,                            -- курсы единиц на момент создания (ADR-47)
    priority        INT  NOT NULL DEFAULT 0,
    group_id        UUID,                             -- вектор задач (ADR-36)
    group_pos       INT  NOT NULL DEFAULT 0,
    retry_of        UUID,                             -- без FK: ретеншн удаляет старые задачи (ADR-37)
    locked_by       UUID REFERENCES service_accounts(id),
    locked_until    TIMESTAMPTZ,
    accepted        BOOLEAN NOT NULL DEFAULT FALSE,
    eta_seconds     INT,
    attempts        INT  NOT NULL DEFAULT 0,
    verdict         TEXT,
    report          JSONB,
    result          JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    UNIQUE (user_id, idempotency_key),
    FOREIGN KEY (form_id, schema_version) REFERENCES forms(form_id, version)
);
-- Очередь: SKIP LOCKED по (service, version) с приоритетом (ADR-08/19/36).
CREATE INDEX system_tasks_queue ON system_tasks (target_service, schema_version, priority DESC, created_at)
    WHERE status = 'pending';
CREATE INDEX system_tasks_locks ON system_tasks (locked_until) WHERE status = 'processing';
CREATE INDEX system_tasks_user ON system_tasks (user_id, created_at DESC);
CREATE INDEX system_tasks_group ON system_tasks (group_id, group_pos) WHERE group_id IS NOT NULL;
CREATE INDEX system_tasks_finished ON system_tasks (finished_at) WHERE finished_at IS NOT NULL;

-- Трасса: append-only, TTL — фоновое задание (ADR-24).
CREATE TABLE task_trace (
    id       BIGSERIAL PRIMARY KEY,
    task_id  UUID NOT NULL,
    at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    stage    TEXT NOT NULL,
    actor    TEXT NOT NULL,
    code     TEXT,
    detail   JSONB NOT NULL DEFAULT '{}'
);
CREATE INDEX task_trace_task ON task_trace (task_id, id);
CREATE INDEX task_trace_at ON task_trace (at);

-- Outbox и сквозной seq на пользователя (ADR-05/23/46).
CREATE TABLE outbox_queue (
    id          BIGSERIAL PRIMARY KEY,
    user_id     UUID NOT NULL,
    type        TEXT NOT NULL,
    payload     JSONB NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent')),
    seq         BIGINT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at     TIMESTAMPTZ
);
CREATE INDEX outbox_pending ON outbox_queue (id) WHERE status = 'pending';
CREATE UNIQUE INDEX outbox_user_seq ON outbox_queue (user_id, seq) WHERE seq IS NOT NULL;

CREATE TABLE user_seq (
    user_id  UUID PRIMARY KEY,
    last_seq BIGINT NOT NULL DEFAULT 0
);
