# offgrid-front — SDK фронта

Рисует разметку подразделений (ADR-72): `card` — карточку факта, `section` — раздел кабинета, `menu` — пункт
меню. Чистый DOM, без фреймворка: подходит и экрану на React, и экрану на обычном TypeScript.

```ts
import { findView, renderSection, viewsOf, type ServiceViews } from "./offgrid";

const all: ServiceViews[] = (await (await fetch("/api/v1/views")).json()).items;
for (const section of viewsOf(all, "section")) {
  const card = findView(all, "card", section.card)!;
  const facts = await myFactsOf(section.form);          // факты пользователя этого бланка
  page.append(renderSection(section, card, facts, { onAction: (a, v) => go(a, v) }));
}
```

- Значения берутся из полей факта по имени; текст — только `textContent`, HTML из данных не вставляется.
- Стиль — классы `og-*`: цвета, шрифты, отступы задаёт страница. Подразделение их не переопределяет.
- Значки меню — общий набор `ICONS`; свои значки подразделения не присылают.
- Действия карточки (`open`, `continue`, `screen`) выполняет страница — SDK только сообщает, что нажали.

Подключение в подразделении: скопировать `pkg/sdk/ts/src` нужной версии ядра (`BHENDHO_REF`) в свой экран,
например при сборке образа из архива GitHub. Проверка — `make test` ядра (сервис `ts-sdk-test`).

## Клиент API людей (`people.ts`, ADR-81)

Один клиент ядра для экрана подразделения, лика и панели — типы из договора `contracts/interfaces/people.openapi.yaml`:

```ts
import { People, PeopleError, type Schemas } from "./offgrid";

const people = new People({ base: () => "", token: () => lik.account.token(), refresh: () => lik.account.refresh() });
const me: Schemas["Profile"] = await people.call("get", "/api/v1/me");
const fact = await people.call("get", "/api/v1/facts/{id}", { path: { id } });
await people.call("post", "/api/v1/tasks", { body: { form_id, values, idempotency_key } });
```

- путь, строка запроса, тело и ответ проверяет `tsc` по договору: поле переименовали в ядре — не собирается экран;
- 401 — одна попытка `refresh()` (ключ обновляет хозяин сессии — лик) и повтор; отказ — `PeopleError` (`status`, `code` — код реестра или `error.code`, `report`, `fields`);
- `people.gen.ts` — из договора: `make people-types`; `make test` ядра падает, если он устарел;
- зависимостей во время работы нет: исходники SDK экраны копируют к себе (ADR-72).
