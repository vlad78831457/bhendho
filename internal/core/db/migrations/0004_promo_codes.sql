-- Промокоды (ADR-57): деньги принимает отдельная служба оплаты (эмитент), ядро видит только
-- выпущенные ею коды. Погашение — депозит провайдера promo (контракт депозита ADR-26).

-- Эмитенты — службы оплаты (или оператор). Токен показывается один раз, в БД — только хэш.
CREATE TABLE promo_issuers (
    id          UUID PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    token_hash  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at  TIMESTAMPTZ
);

CREATE TABLE promo_codes (
    id           UUID PRIMARY KEY,
    issuer_id    UUID NOT NULL REFERENCES promo_issuers(id),
    code_hash    TEXT NOT NULL UNIQUE,            -- sha256 нормализованного кода; сам код не хранится
    external_id  TEXT,                            -- номер заказа у эмитента: повторный выпуск не создаёт второй код
    amount       NUMERIC(15,4) NOT NULL CHECK (amount > 0),
    currency_id  TEXT NOT NULL REFERENCES currencies(id),
    for_email    TEXT,                            -- персональный код: погасить может только этот email
    status       TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'redeemed', 'revoked')),
    expires_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    redeemed_by  UUID REFERENCES users(id),
    redeemed_at  TIMESTAMPTZ,
    deposit_id   UUID REFERENCES deposits(id),
    revoked_at   TIMESTAMPTZ,
    UNIQUE (issuer_id, external_id),
    CHECK ((status = 'redeemed') = (redeemed_by IS NOT NULL AND deposit_id IS NOT NULL))
);

-- Неверные попытки ввода — ограничение подбора (десятки попыток в минуту кодом не угадать).
CREATE TABLE promo_failures (
    user_id  UUID NOT NULL,
    at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX promo_failures_user ON promo_failures (user_id, at);
