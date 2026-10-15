-- Тарифы в панели админа (ADR-69): цена бланка, курсы единиц подразделения, множитель минуса.
-- Журнал изменений только дописывается; действует последняя запись по (scope, target); value NULL —
-- вернуть значение из паспорта модуля или настроек. Регистрация модулей эту таблицу не трогает.
CREATE TABLE tariff_changes (
    id          BIGSERIAL PRIMARY KEY,
    scope       TEXT NOT NULL CHECK (scope IN ('form_price', 'unit_rate', 'overdraft_factor')),
    target      TEXT NOT NULL DEFAULT '',   -- form_id | <подразделение>:<единица> | ''
    value       NUMERIC(15,6) CHECK (value > 0),
    writer      TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX tariff_changes_target ON tariff_changes (scope, target, id DESC);
CREATE TRIGGER tariff_changes_append_only BEFORE UPDATE OR DELETE ON tariff_changes
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE VIEW tariffs AS
    SELECT DISTINCT ON (scope, target) scope, target, value
    FROM tariff_changes ORDER BY scope, target, id DESC;
