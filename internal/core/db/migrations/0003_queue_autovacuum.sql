-- Очереди в Postgres (ADR-55): задачи и outbox каждую секунду переходят между статусами, и частичные
-- индексы (status = 'pending') быстро набирают мёртвые записи. Стандартные пороги autovacuum
-- (20 % таблицы) для очереди слишком редки: claim начинает просматривать мусор.
ALTER TABLE system_tasks SET (
    autovacuum_vacuum_scale_factor = 0.01, autovacuum_vacuum_threshold = 500,
    autovacuum_analyze_scale_factor = 0.02, autovacuum_vacuum_cost_limit = 2000);
ALTER TABLE outbox_queue SET (
    autovacuum_vacuum_scale_factor = 0.01, autovacuum_vacuum_threshold = 500,
    autovacuum_analyze_scale_factor = 0.02, autovacuum_vacuum_cost_limit = 2000);
-- Кошельки обновляются на каждой денежной операции: запас места на странице — для HOT-обновлений.
ALTER TABLE wallets SET (fillfactor = 80, autovacuum_vacuum_scale_factor = 0.02, autovacuum_vacuum_threshold = 200);
