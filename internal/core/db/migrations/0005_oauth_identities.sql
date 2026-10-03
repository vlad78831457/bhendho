-- Вход через внешние аккаунты (ADR-58): Google, VK ID и любые OpenID Connect — привязки к пользователю.
-- Пользователь из VK может прийти без почты, а у вошедшего через провайдера нет пароля.
ALTER TABLE users ALTER COLUMN email DROP NOT NULL;
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;

-- Привязка: аккаунт у провайдера (provider + subject — его неизменный id) → наш пользователь.
-- У пользователя может быть несколько привязок (Google и VK) и пароль одновременно.
CREATE TABLE user_identities (
    provider       TEXT NOT NULL,
    subject        TEXT NOT NULL,
    user_id        UUID NOT NULL REFERENCES users(id),
    email          TEXT,
    email_verified BOOLEAN NOT NULL DEFAULT FALSE,
    display_name   TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, subject)
);
CREATE INDEX user_identities_user ON user_identities (user_id);

-- Начатый вход: state (одноразовый, в БД — хэш), PKCE verifier и nonce, куда вернуть пользователя.
CREATE TABLE oauth_states (
    state_hash  TEXT PRIMARY KEY,
    provider    TEXT NOT NULL,
    verifier    TEXT NOT NULL,
    nonce       TEXT NOT NULL,
    return_to   TEXT NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL
);

-- Одноразовый код, которым фронт забирает токены после возврата от провайдера (токены не идут в адресе).
CREATE TABLE oauth_logins (
    code_hash   TEXT PRIMARY KEY,
    user_id     UUID NOT NULL REFERENCES users(id),
    expires_at  TIMESTAMPTZ NOT NULL
);
