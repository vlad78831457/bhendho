-- Факты подразделений (ADR-65): то, что подразделение помнит для себя и своих админов, — расход токенов,
-- модель, тексты промптов, сведения о медиа. Ядро их не понимает: хранит как прислали и ставит семь полей само.
-- «Кому» — свободное слово из бланка: user — факт пользователя (как раньше), остальное — сюда.
ALTER TABLE forms ADD COLUMN audience TEXT NOT NULL DEFAULT 'user' CHECK (audience <> '');

CREATE TABLE service_facts (
    id              BIGSERIAL PRIMARY KEY,
    service_id      UUID NOT NULL REFERENCES services(id),       -- подразделение
    form_id         TEXT NOT NULL,                               -- бланк
    schema_version  INT  NOT NULL,                               -- его версия
    audience        TEXT NOT NULL,                               -- «кому» из бланка
    task_id         UUID,                                        -- задача; без FK: задачи живут 14 дней (ADR-37)
    user_id         UUID REFERENCES users(id) ON DELETE SET NULL, -- для кого делали; пусто — не про человека
    prorab          TEXT NOT NULL,                               -- кто прислал
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),          -- время записи
    values          JSONB NOT NULL                               -- содержимое как прислал прораб
);
CREATE INDEX service_facts_lookup ON service_facts (service_id, form_id, created_at DESC);
CREATE INDEX service_facts_at ON service_facts (created_at);
