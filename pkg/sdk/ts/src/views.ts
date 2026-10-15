// Разметка общей территории фронта (ADR-72 ядра). Подразделение описывает, что показать; рисует оболочка,
// стиль общий — классы og-*, цвета и шрифты задаёт страница. Значения берутся из полей факта по имени.

export type CardElement =
  | { title: string }
  | { text: string; lines?: number }
  | { image: string }
  | { facts: string[] }
  | { badge: string }
  | { action: "open" | "continue" | "screen"; label?: string; form?: string; path?: string };

export interface CardView { id: string; kind: "card"; form: string; body: CardElement[] }
export interface SectionView { id: string; kind: "section"; form: string; title: string; card: string; order?: "created_at" | "-created_at" }
export interface MenuView {
  id: string; kind: "menu"; title: string; icon?: string;
  target: { form?: string; section?: string; screen?: string };
}
export type View = CardView | SectionView | MenuView;

/** Разметка одного подразделения — ответ GET /api/v1/views. */
export interface ServiceViews { service: string; title: string; views: View[] }

/** Действие карточки, которое выполняет страница: открыть факт, продолжить задачей, перейти на свой экран. */
export interface CardAction { kind: "open" | "continue" | "screen"; form?: string; path?: string }

export interface RenderOptions {
  /** Ссылка на картинку из значения поля: строка-адрес или файл ядра. Нет — картинки не будет. */
  imageUrl?: (value: unknown) => string | undefined;
  /** Подпись поля в «ключ: значение»; по умолчанию — имя поля. */
  label?: (field: string) => string;
  onAction?: (action: CardAction, values: Record<string, unknown>) => void;
}

/** Общий набор значков оболочки: подразделения свои значки не присылают (ответ директора 2026-10-15). */
export const ICONS: Record<string, string> = {
  book: "📖", user: "🙂", wallet: "👛", star: "⭐", image: "🖼", music: "🎵", list: "☰", settings: "⚙", help: "?",
};

const ACTION_LABEL: Record<CardAction["kind"], string> = { open: "Открыть", continue: "Продолжить", screen: "Перейти" };

function el(tag: string, cls: string, text?: string): HTMLElement {
  const e = document.createElement(tag);
  e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

const str = (v: unknown): string => (v === null || v === undefined ? "" : typeof v === "object" ? JSON.stringify(v) : String(v));

/** Карточка факта по разметке card. Пустые значения пропускаются; текст — только textContent, без HTML. */
export function renderCard(view: CardView, values: Record<string, unknown>, opts: RenderOptions = {}): HTMLElement {
  const card = el("article", "og-card");
  card.dataset.view = view.id;
  for (const item of view.body) {
    if ("title" in item) {
      const v = str(values[item.title]);
      if (v) card.append(el("h3", "og-title", v));
    } else if ("text" in item) {
      const v = str(values[item.text]);
      if (v) {
        const p = el("p", "og-text", v);
        p.style.setProperty("--og-lines", String(item.lines ?? 3));
        card.append(p);
      }
    } else if ("image" in item) {
      const src = opts.imageUrl ? opts.imageUrl(values[item.image]) : typeof values[item.image] === "string" ? (values[item.image] as string) : undefined;
      if (src) {
        const img = el("img", "og-image") as HTMLImageElement;
        img.src = src;
        img.alt = "";
        img.loading = "lazy";
        card.append(img);
      }
    } else if ("facts" in item) {
      const dl = el("dl", "og-facts");
      for (const f of item.facts) {
        const v = str(values[f]);
        if (!v) continue;
        dl.append(el("dt", "og-fact-key", opts.label ? opts.label(f) : f), el("dd", "og-fact-value", v));
      }
      if (dl.childElementCount) card.append(dl);
    } else if ("badge" in item) {
      const v = str(values[item.badge]);
      if (v) card.append(el("span", "og-badge", v));
    } else if ("action" in item) {
      const b = el("button", "og-action", item.label ?? ACTION_LABEL[item.action]) as HTMLButtonElement;
      b.type = "button";
      b.onclick = () => opts.onAction?.({ kind: item.action, form: item.form, path: item.path }, values);
      card.append(b);
    }
  }
  return card;
}

/** Факт для раздела: id, время и значения опубликованной версии. */
export interface SectionItem { fact_id: string; created_at: string; values: Record<string, unknown> }

/** Раздел кабинета по разметке section: заголовок и карточки в заданном порядке. */
export function renderSection(section: SectionView, card: CardView, items: SectionItem[], opts: RenderOptions = {}): HTMLElement {
  const root = el("section", "og-section");
  root.dataset.view = section.id;
  root.append(el("h2", "og-section-title", section.title));
  const sorted = [...items].sort((a, b) =>
    section.order === "created_at" ? a.created_at.localeCompare(b.created_at) : b.created_at.localeCompare(a.created_at),
  );
  if (!sorted.length) root.append(el("p", "og-empty", "Пока пусто"));
  for (const it of sorted) {
    const c = renderCard(card, it.values, opts);
    c.dataset.fact = it.fact_id;
    root.append(c);
  }
  return root;
}

/** Пункты меню всех подразделений; куда вести — решает страница по target. */
export function renderMenu(items: MenuView[], onSelect: (item: MenuView) => void): HTMLElement {
  const nav = el("nav", "og-menu");
  for (const m of items) {
    const b = el("button", "og-menu-item") as HTMLButtonElement;
    b.type = "button";
    if (m.icon && ICONS[m.icon]) b.append(el("span", "og-icon", ICONS[m.icon]));
    b.append(document.createTextNode(m.title));
    b.onclick = () => onSelect(m);
    nav.append(b);
  }
  return nav;
}

/** Найти разметку по id во всех подразделениях. */
export function findView<K extends View["kind"]>(all: ServiceViews[], kind: K, id: string): Extract<View, { kind: K }> | undefined {
  for (const s of all) for (const v of s.views) if (v.kind === kind && v.id === id) return v as Extract<View, { kind: K }>;
  return undefined;
}

/** Все разметки вида kind, например все разделы кабинета или все пункты меню. */
export function viewsOf<K extends View["kind"]>(all: ServiceViews[], kind: K): Extract<View, { kind: K }>[] {
  return all.flatMap((s) => s.views.filter((v): v is Extract<View, { kind: K }> => v.kind === kind));
}
