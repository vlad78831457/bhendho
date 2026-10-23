-- Публичные факты (ADR-79): на опубликованную версию публичного факта может сослаться любой пользователь —
-- в задаче и в своём факте. Публичным делает только админ и только свой факт. По умолчанию факт приватный.
ALTER TABLE facts ADD COLUMN visibility TEXT NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'public'));
CREATE INDEX facts_public ON facts (kind) WHERE visibility = 'public';
