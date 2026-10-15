-- Разметка общей территории фронта (ADR-72): как есть из паспорта подразделения.
ALTER TABLE services ADD COLUMN views JSONB NOT NULL DEFAULT '[]';
