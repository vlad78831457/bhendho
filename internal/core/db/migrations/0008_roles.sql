-- Роли (ADR-61): user — обычный пользователь, tester — тестовый, admin — видит панель админа.
-- Роль меняется только командой на сервере (core user-role), через API — никогда.
ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'tester', 'admin'));
