-- Факт подразделения может записать и админ (ADR-66): «кто прислал» — prorab:<имя> или admin:<id>.
ALTER TABLE service_facts RENAME COLUMN prorab TO writer;
