import { clean, initialValues, validate } from "../../src/lib/schema";
import type { Rules } from "../../src/lib/types";

// Та же форма, что в тестах ядра (internal/core/forms): пути ошибок должны совпадать с серверными.
const rules: Rules = {
  title: { type: "string", required: true, min: 1, max: 20 },
  visits: { type: "int", min: 0, max: 10 },
  score: { type: "float" },
  on: { type: "date" },
  level: { type: "select", options: ["low", "high"], default: "low" },
  tags: { type: "multiselect", options: ["a", "b"] },
  hero: { type: "fact_ref", required: true, expected_fact_kind: "tale.character.v1" },
  address: { type: "group", items: { city: { type: "string", required: true } } },
  notes: { type: "list", min: 1, max: 2, items: { text: { type: "text", required: true } } },
  extra: { type: "mix" },
};

const HERO = "0b6c1f4e-8f0e-4a55-b2a7-3e9d1c7f2a01";

test("initial values come from defaults, lists and groups", () => {
  expect(initialValues(rules)).toEqual({ level: "low", tags: [], address: {}, notes: [] });
});

test("valid values pass", () => {
  const v = { title: "Осмотр", visits: 3, score: 4.5, on: "2026-09-28", level: "high", tags: ["a"], hero: HERO,
              address: { city: "Сочи" }, notes: [{ text: "x" }], extra: { any: [1] } };
  expect(validate(rules, v)).toEqual([]);
});

test("errors use the same paths as the core", () => {
  const errs = validate(rules, { title: "я".repeat(21), visits: 1.5, level: "mid", hero: "Р-014",
                                 address: {}, notes: [{}], tags: ["z"] });
  expect(errs.map((e) => e.field).sort()).toEqual(
    ["address.city", "hero", "level", "notes[0].text", "tags", "title", "visits"].sort(),
  );
  expect(validate(rules, { hero: HERO, notes: [], address: { city: "x" }, title: "t" })).toEqual([
    { field: "notes", message: "строк не меньше 1" },
  ]);
});

test("clean drops empty optional values (core materializes only what is written)", () => {
  expect(clean(rules, { title: "t", visits: undefined, tags: [], address: { city: "" }, notes: [{ text: "a" }],
                        hero: HERO, on: "" }))
    .toEqual({ title: "t", notes: [{ text: "a" }], hero: HERO });
});
