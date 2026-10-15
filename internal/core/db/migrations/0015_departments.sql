-- Подразделения подключаются сами (ADR-70): админ выдаёт ключ на код подразделения; с ним подразделение
-- регистрирует свой паспорт и формы и работает как прораб. Выключатель — services.enabled.
ALTER TABLE services ADD COLUMN enabled BOOLEAN NOT NULL DEFAULT TRUE;

CREATE TABLE department_keys (
    id          UUID PRIMARY KEY,
    code        TEXT NOT NULL,              -- код подразделения = worker.target_service паспорта
    name        TEXT NOT NULL,              -- имя прораба, под которым подразделение берёт задачи
    token_hash  TEXT NOT NULL UNIQUE,
    created_by  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at  TIMESTAMPTZ,
    UNIQUE (code, name)
);
