# Вход через Google, VK ID и другие аккаунты

Кроме email и пароля человек может войти кнопкой «Войти через Google» или «Войти через VK ID». Провайдеров может быть сколько угодно: кнопка появляется, как только у провайдера есть ключи. Решение — ADR-58.

## Как это работает

1. Человек нажимает кнопку → ядро запоминает одноразовый «билет» входа (на 10 минут) и отправляет браузер к провайдеру.
2. Провайдер спрашивает логин и согласие и возвращает браузер к ядру с кодом.
3. Ядро само, без браузера, меняет код на данные человека у провайдера и проверяет их (для Google — подпись, кому выдано, срок).
4. Ядро находит или создаёт пользователя и возвращает браузер на страницу, откуда он ушёл. Сессия выдаётся через одноразовый код на 2 минуты — токены не попадают в адресную строку.

Кто есть кто:
- **Тот же внешний аккаунт** — всегда тот же пользователь.
- **Google** подтверждает email. Если аккаунт с таким email уже есть (регистрация по паролю), вход через Google попадёт в него. Если нет — новый пользователь с этим email.
- **VK ID** не сообщает, подтверждён ли email. Поэтому вход через VK всегда создаёт своего пользователя без email: иначе можно было бы войти в чужой аккаунт, указав в VK чужой адрес. В шапке тогда показывается имя.
- **Несколько входов у одного пользователя** — в кабинете: «Привязать Google» или «Привязать VK ID». Если у пользователя не было email, а Google его подтвердил и адрес свободен, email появится у пользователя. Внешний аккаунт, уже привязанный к другому пользователю, не перепривязывается. Последний способ входа отвязать нельзя.
- **Пароль** можно задать в кабинете, если у пользователя есть email (например, после привязки Google).

## Что вписать в `.env`

Ключи — только в `ecosystem/.env` (или `.env` рядом с тем compose, которым запускаете). Не в код и не в чат.

```
PUBLIC_URL=https://сказки.example.ru      # адрес сайта снаружи; для localhost можно оставить пустым
OAUTH_GOOGLE_CLIENT_ID=...
OAUTH_GOOGLE_CLIENT_SECRET=...
OAUTH_VK_CLIENT_ID=...
OAUTH_VK_CLIENT_SECRET=...                # ядру для входа не нужен, но пусть хранится рядом
```

После правки — `make up`. Проверка: `curl -s localhost:3000/api/v1/auth/providers` покажет включённых провайдеров.

**Адрес возврата** (его нужно вписать у провайдера) — `PUBLIC_URL` + `/api/v1/auth/oauth/<провайдер>/callback`:
- Google: `https://сказки.example.ru/api/v1/auth/oauth/google/callback`
- VK ID: `https://сказки.example.ru/api/v1/auth/oauth/vk/callback`
- для проверки на своём компьютере: `http://localhost:3000/api/v1/auth/oauth/google/callback`

Адрес должен совпадать с тем, что вписан у провайдера, до символа: иначе провайдер откажет.

## Google — получить ключи

Интерфейс консоли Google время от времени меняется, названия пунктов могут чуть отличаться.

1. https://console.cloud.google.com → создать проект (например, «Taleweaver»).
2. Раздел **Google Auth Platform** (раньше — «OAuth consent screen»): название приложения, email поддержки. Аудитория — **External**. Пока приложение в режиме «Testing», войти могут только добавленные вами тестовые пользователи. Чтобы входили все, нажмите «Publish app». Для доступа только к email и имени проверка Google обычно не нужна.
3. **Clients → Create client**, тип **Web application**.
4. **Authorized redirect URIs** — адрес возврата выше. Для localhost Google разрешает `http://`, для остальных адресов нужен `https://`.
5. Скопировать **Client ID** и **Client secret** в `OAUTH_GOOGLE_CLIENT_ID` и `OAUTH_GOOGLE_CLIENT_SECRET`.

## VK ID — получить ключи

1. https://id.vk.com/business/go → войти → «Создать приложение», платформа **Web**.
2. Указать базовый домен сайта и **доверенный Redirect URL** — адрес возврата выше.
3. В настройках доступа отметить **email**, если нужен (VK может его и не отдать).
4. Скопировать **ID приложения** в `OAUTH_VK_CLIENT_ID`, а **защищённый ключ** — в `OAUTH_VK_CLIENT_SECRET`.

VK ID работает по OAuth 2.1 с PKCE: секрет приложения при входе не передаётся. На `localhost` VK может не пустить. Тогда проверять на сервере с доменом (`try/an-cloud`) или через тестовый провайдер (ниже).

## Другие провайдеры

Любой провайдер со стандартом **OpenID Connect** подключается без кода — строкой в `OAUTH_PROVIDERS` (JSON-массив):

```
OAUTH_PROVIDERS=[{"id":"company","name":"Вход сотрудника","kind":"oidc","issuer":"https://sso.example.ru/realms/main","client_id":"...","client_secret":"...","trust_email":false}]
```

- `id` — латиницей, попадает в адрес возврата (`/api/v1/auth/oauth/company/callback`);
- `issuer` — адрес, у которого есть `/.well-known/openid-configuration`;
- `trust_email: true` — только если провайдер гарантирует, что email подтверждён и принадлежит человеку. Тогда вход привяжется к аккаунту с тем же email.

Перед подключением Яндекс ID, Сбер ID и других нужно проверить, насколько они следуют OpenID Connect. Если протокол свой, как у VK ID, нужен маленький адаптер в `internal/core/auth/oauth/` (пример — `vkid.go`).

**Apple** — отдельный адаптер (позже): у Apple секрет клиента — это JWT, подписанный ключом из кабинета разработчика. Возврат приходит POST-запросом, а имя человека присылается только при первом входе. Нужен платный Apple Developer Program.

## Проверить без настоящих ключей

Тестовый провайдер `oauthmock` изображает Google и VK ID. Настоящих аккаунтов у него нет: на странице входа вы сами вписываете, кем войти. На нём работают сквозные тесты (`make e2e`). Запустить вручную:

```
# в ecosystem/.env:
OAUTH_GOOGLE_CLIENT_ID=offgrid-dev
OAUTH_GOOGLE_ISSUER=http://oauthmock:8090
OAUTH_VK_CLIENT_ID=offgrid-dev
OAUTH_VK_BASE_URL=http://oauthmock:8090/vk
OAUTHMOCK_PUBLIC_URL=http://localhost:8092

docker compose --profile oauthmock up -d --build     # плюс прокинуть порт 8092, как 3000
```

В продакшн `oauthmock` не ставится: без этих строк в `.env` он не нужен.

## Для разработчика

- API: `GET /api/v1/auth/providers`, `GET /api/v1/auth/oauth/{id}/start?return_to=/путь`, `GET …/{id}/callback` (302 на `return_to#oauth=<код>` или `#oauth_error=denied|expired|provider`), `POST /api/v1/auth/oauth/exchange {"code"}` → токены, как у `/login`.
- Кабинет: `GET /api/v1/me/identities`, `POST /api/v1/me/identities/{id}/link {"return_to"}` → `{"url"}` (браузер уходит по нему; возврат — `#oauth_linked=<id>` или `#oauth_link_error=taken|denied|expired|provider`), `DELETE /api/v1/me/identities/{id}/{subject}` (последний вход — 409), `PATCH /api/v1/me {"display_name"}`, `POST /api/v1/me/password {"current_password","new_password"}`.
- `return_to` — только путь этого же сайта: чужой адрес заменяется на `/`.
- Таблицы: `user_identities` (провайдер + id у провайдера → пользователь), `oauth_states`, `oauth_logins` (одноразовые, хранятся хэши). Миграции `0005`, `0006` (`oauth_states.link_user` — привязка из кабинета).
- Настройки ядра: `CORE_PUBLIC_URL`, `CORE_OAUTH_GOOGLE_*`, `CORE_OAUTH_VK_*`, `CORE_OAUTH_PROVIDERS` (compose пробрасывает их из `.env` без префикса `CORE_`).
