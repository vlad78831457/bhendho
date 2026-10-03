-- ADR-51: форма задачи объявляет факт-результат.
ALTER TABLE forms ADD COLUMN result_spec JSONB;
