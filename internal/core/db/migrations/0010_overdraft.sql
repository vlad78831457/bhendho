-- Минус до множителя резерва (ADR-63): прораб может списать больше резерва, но не больше
-- резерв × CORE_OVERDRAFT_FACTOR; кошелёк пользователя уходит в минус, новые задачи тогда не создаются.
-- Часть сверх резерва — отдельная запись журнала overcharge: она берётся из баланса, а не из резерва.
ALTER TABLE wallets DROP CONSTRAINT wallets_balance_check;
ALTER TABLE wallets DROP CONSTRAINT wallets_check;
ALTER TABLE transactions DROP CONSTRAINT transactions_type_check;
ALTER TABLE transactions ADD CONSTRAINT transactions_type_check
    CHECK (type IN ('deposit', 'reserve', 'charge', 'overcharge', 'release', 'income', 'refund', 'adjust'));
