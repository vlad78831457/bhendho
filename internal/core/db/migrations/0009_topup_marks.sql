-- Метки пополнения (ADR-62): кнопка «Пополнить» получает от ядра одноразовую метку и уносит её в службу
-- оплаты; служба гасит код по метке — без email. Метка привязывается к первому погашенному коду:
-- повтор вебхука с тем же кодом безопасен, второй код по той же метке не пройдёт.
CREATE TABLE topup_marks (
    id          UUID PRIMARY KEY,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    mark_hash   TEXT NOT NULL UNIQUE,                 -- sha256 метки; сама метка не хранится
    expires_at  TIMESTAMPTZ NOT NULL,
    code_id     UUID REFERENCES promo_codes(id),      -- код, погашенный по метке
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    used_at     TIMESTAMPTZ
);
CREATE INDEX topup_marks_expires ON topup_marks (expires_at) WHERE code_id IS NULL;
