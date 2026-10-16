-- Прораб о себе (план 7.6, ADR-77): название и описание присылает подразделение при подключении;
-- время последней связи — по выдаче задач. Админ видит, кто есть кто и жив ли он.
ALTER TABLE service_accounts ADD COLUMN title TEXT NOT NULL DEFAULT '' CHECK (length(title) <= 80),
    ADD COLUMN description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 500),
    ADD COLUMN last_seen_at TIMESTAMPTZ;
