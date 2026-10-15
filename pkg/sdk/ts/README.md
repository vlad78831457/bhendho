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
