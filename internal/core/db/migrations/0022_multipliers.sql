-- Три коэффициента цены (ADR-78, план движка К1): ядро (k GC), подразделение, прораб. По умолчанию 1.
-- Ставит админ в «Тарифах»: scope = 'multiplier', target = 'core' | '<подразделение>' | '<подразделение>:<прораб>'.
ALTER TABLE tariff_changes DROP CONSTRAINT tariff_changes_scope_check;
ALTER TABLE tariff_changes ADD CONSTRAINT tariff_changes_scope_check
    CHECK (scope IN ('form_price', 'unit_rate', 'overdraft_factor', 'multiplier'));
