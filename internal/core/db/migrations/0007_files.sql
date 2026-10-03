-- Файлы пользователей (ADR-60): реестр — таблица files из 0001 (ADR-16). Квота считается по
-- живым файлам пользователя; размеры картинки — для подсказок фронту.
CREATE INDEX files_user_active ON files (user_id) WHERE status = 'active';
ALTER TABLE files ADD COLUMN width INT, ADD COLUMN height INT;
