import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";

import { DynamicForm } from "../../src/components/DynamicForm";
import { Markdown } from "../../src/components/Markdown";
import { ReportView } from "../../src/components/ReportView";
import { ResultView } from "../../src/components/ResultView";
import { Api } from "../../src/lib/api";
import type { Values } from "../../src/lib/schema";
import type { Envelope, FieldError, Rules } from "../../src/lib/types";

const api = new Api("", (async () => new Response(JSON.stringify({ items: [] }))) as unknown as typeof fetch);

function Harness({ rules, errors = [], onValues }: { rules: Rules; errors?: FieldError[]; onValues: (v: Values) => void }) {
  const [v, setV] = useState<Values>({ rows: [] });
  return <DynamicForm rules={rules} values={v} errors={errors} api={api} onChange={(x) => (setV(x), onValues(x))} />;
}

test("renders every field type from rules and edits values", async () => {
  const user = userEvent.setup();
  let last: Values = {};
  const rules: Rules = {
    name: { type: "string", required: true, desc: "Имя" },
    count: { type: "int", min: 1 },
    ok: { type: "boolean" },
    mode: { type: "select", options: ["a", "b"] },
    rows: { type: "list", items: { note: { type: "text" } } },
  };
  render(<Harness rules={rules} onValues={(v) => (last = v)} />);
  await user.type(screen.getByLabelText(/name/), "Лисёнок");
  await user.type(screen.getByLabelText("count"), "3");
  await user.click(screen.getByLabelText("ok"));
  await user.selectOptions(screen.getByLabelText("mode"), "b");
  await user.click(screen.getByRole("button", { name: "Добавить строку" }));
  await user.type(screen.getByLabelText("note"), "x");
  expect(last).toEqual({ rows: [{ note: "x" }], name: "Лисёнок", count: 3, ok: true, mode: "b" });
  expect(screen.getByText("Имя")).toBeTruthy();
});

test("server field errors land on their fields accessibly", () => {
  render(<Harness rules={{ start: { type: "int" } }} errors={[{ field: "start", message: "value must be at least 1" }]}
    onValues={() => {}} />);
  const input = screen.getByLabelText("start");
  expect(input.getAttribute("aria-invalid")).toBe("true");
  expect(screen.getByRole("alert").textContent).toBe("value must be at least 1");
});

const envelope = (report: Envelope["report"], result: Record<string, unknown>): Envelope => ({
  system: { document_id: "t", form_id: "f", schema_version: 1, kind: "task", status: "completed", created_at: "" },
  meta_ui: { title: "t", fields_rules: {} }, commands: {}, values: {}, report, result,
});

test("report shows verdict, explanation, cards with text levels and data gaps", () => {
  render(<ReportView env={envelope({ verdict: "SUCCESS", user_message: "Готово" }, {
    explanation: { formulas: ["прогноз = расход × дни"], as_of: "2026-09-15", summary: "Нужно 75 л" },
    cards: [{ level: "alert", title: "Недосып → срывы", how_computed: "8 из 8", confidence: 0.9, sample_size: 30,
              evidence: [{ observation_id: 4, date: "2025-03-04" }], recommendations: ["Обсудить режим сна"] }],
    data_gaps: [{ level: "warning", message: "Сон не указан в 5 днях" }],
  })} />);
  expect(screen.getByText("прогноз = расход × дни")).toBeTruthy();
  expect(screen.getByText("Требует внимания")).toBeTruthy(); // уровень словом, не только цветом
  expect(screen.getByText("Записи: №4 (2025-03-04)")).toBeTruthy();
  expect(screen.getByRole("note").textContent).toContain("Сон не указан в 5 днях");
});

test("rejected data shows the registry verdict and row issues", () => {
  render(<ReportView env={envelope({ verdict: "DATA_REJECTED", user_message: "исправьте", admin_message: "consume > stock",
    issues: [{ index: 1, message: "consume 99999 больше доступного 50" }] } as Envelope["report"], {})} />);
  expect(screen.getByText("Данные не подходят")).toBeTruthy();
  expect(screen.getByText("Строка 2: consume 99999 больше доступного 50")).toBeTruthy();
});

test("generic result: tables for records, matrices, null as 'нет данных'", () => {
  render(<ResultView result={{ path: [1, 3, 2], matrix: [[0, null], [2, 0]], items: [{ sku: "OIL", qty: 75 }], distance: null }} />);
  expect(screen.getByText("1, 3, 2")).toBeTruthy();
  expect(screen.getAllByText("нет данных").length).toBe(2);
  expect(screen.getByRole("columnheader", { name: "sku" })).toBeTruthy();
});

test("markdown renders tables and never injects HTML", () => {
  const { container } = render(<Markdown text={"# Отчёт\n\n| a | b |\n|---|---|\n| **1** | 2 |\n\n- <img src=x onerror=alert(1)>"} />);
  expect(container.querySelector("h2")?.textContent).toBe("Отчёт");
  expect(container.querySelector("td strong")?.textContent).toBe("1");
  expect(container.querySelector("img")).toBeNull();
});
