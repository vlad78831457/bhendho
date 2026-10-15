import { findView, renderCard, renderMenu, renderSection, viewsOf, type CardView, type SectionView, type ServiceViews } from "../src";

const card: CardView = {
  id: "hero.card", kind: "card", form: "taleweaver.character.v1",
  body: [{ image: "photo" }, { title: "name" }, { text: "description", lines: 2 }, { facts: ["role", "age"] }, { badge: "status" },
    { action: "open" }, { action: "continue", form: "taleweaver.generate_start.v1", label: "Сказка с ним" }],
};
const section: SectionView = { id: "hero.section", kind: "section", form: card.form, title: "Мои герои", card: card.id, order: "-created_at" };

test("card shows fields by name and skips empty ones", () => {
  const c = renderCard(card, { name: "Лисёнок", description: "рыжий", role: "hero", photo: "/media/p.jpg" });
  expect(c.querySelector(".og-title")?.textContent).toBe("Лисёнок");
  expect(c.querySelector(".og-text")?.textContent).toBe("рыжий");
  expect(c.querySelector("img")?.getAttribute("src")).toBe("/media/p.jpg");
  expect([...c.querySelectorAll(".og-fact-key")].map((e) => e.textContent)).toEqual(["role"]);
  expect(c.querySelector(".og-badge")).toBeNull();
});

test("text is never HTML", () => {
  const c = renderCard(card, { name: "<img src=x onerror=alert(1)>" });
  expect(c.querySelector(".og-title img")).toBeNull();
  expect(c.querySelector(".og-title")?.textContent).toBe("<img src=x onerror=alert(1)>");
});

test("actions are handed to the page with the fact values", () => {
  const got: string[] = [];
  const c = renderCard(card, { name: "Ёжик" }, { onAction: (a, v) => got.push(`${a.kind} ${a.form ?? ""} ${String(v.name)}`) });
  const [open, cont] = c.querySelectorAll<HTMLButtonElement>(".og-action");
  expect(open.textContent).toBe("Открыть");
  expect(cont.textContent).toBe("Сказка с ним");
  open.click();
  cont.click();
  expect(got).toEqual(["open  Ёжик", "continue taleweaver.generate_start.v1 Ёжик"]);
});

test("image comes through the page resolver for files", () => {
  const c = renderCard(card, { photo: { file_id: "f1" } }, { imageUrl: (v) => (v as { file_id: string }).file_id && "/signed/f1" });
  expect(c.querySelector("img")?.getAttribute("src")).toBe("/signed/f1");
});

test("section orders cards and says when empty", () => {
  const s = renderSection(section, card, [
    { fact_id: "a", created_at: "2026-10-01T00:00:00Z", values: { name: "Старый" } },
    { fact_id: "b", created_at: "2026-10-02T00:00:00Z", values: { name: "Новый" } },
  ]);
  expect(s.querySelector(".og-section-title")?.textContent).toBe("Мои герои");
  expect([...s.querySelectorAll(".og-title")].map((e) => e.textContent)).toEqual(["Новый", "Старый"]);
  expect(renderSection(section, card, []).querySelector(".og-empty")?.textContent).toBe("Пока пусто");
});

test("menu uses the shell icon set and finds views across departments", () => {
  const all: ServiceViews[] = [{ service: "taleweaver", title: "Сказки", views: [card, section,
    { id: "tale.new", kind: "menu", title: "Новая сказка", icon: "book", target: { form: "taleweaver.generate_start.v1" } },
    { id: "odd", kind: "menu", title: "Без значка", icon: "rocket", target: { section: "hero.section" } }] }];
  const picked: string[] = [];
  const nav = renderMenu(viewsOf(all, "menu"), (m) => picked.push(m.id));
  expect(nav.querySelectorAll(".og-icon").length).toBe(1);
  nav.querySelectorAll<HTMLButtonElement>(".og-menu-item")[0].click();
  expect(picked).toEqual(["tale.new"]);
  expect(findView(all, "card", "hero.card")?.form).toBe("taleweaver.character.v1");
  expect(viewsOf(all, "section").map((s) => s.title)).toEqual(["Мои герои"]);
});
