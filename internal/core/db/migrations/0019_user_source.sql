-- Метка источника (ADR-75): откуда пришёл человек (utm из адреса посадочной страницы). Ставится один раз.
ALTER TABLE users ADD COLUMN source TEXT NOT NULL DEFAULT '' CHECK (length(source) <= 300);
