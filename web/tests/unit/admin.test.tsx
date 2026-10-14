import { fireEvent, render, screen } from "@testing-library/react";

import { App } from "../../src/App";
import { Api } from "../../src/lib/api";
import { SessionProvider } from "../../src/session";

// Простая панель админа (ADR-66): подразделения, память подразделения, настройки.
const services = [{
  code: "taleweaver", title: "Сказки", module_id: "taleweaver/story-engine", module_version: "0.1.0",
  forms: [
    { form_id: "taleweaver.character.v1", version: 1, kind: "fact", audience: "user", title: "Персонаж", price: "0.0000", fields: { name: { type: "string" } } },
    { form_id: "taleweaver.spend.v1", version: 1, kind: "fact", audience: "adm", title: "Расход", price: "0.0000", fields: { model: { type: "string" } } },
  ],
}];

function adminApi(calls: string[]) {
  localStorage.setItem("offgrid.tokens", JSON.stringify({ access_token: "a", refresh_token: "r", expires_in: 60 }));
  const facts = [{ id: 1, service: "taleweaver", form_id: "taleweaver.spend.v1", schema_version: 1, audience: "adm",
    task_id: null, user_id: null, writer: "prorab:prorab-1", created_at: "2026-10-16T10:00:00Z", values: { model: "gpt-x" } }];
  const f = async (url: string | URL | Request, init?: RequestInit) => {
    const u = String(url);
    calls.push(`${init?.method ?? "GET"} ${u} ${init?.body ?? ""}`);
    let body: unknown = { items: [] };
    if (u.endsWith("/api/v1/me")) body = { user_id: "u1", email: "a@b.io", role: "admin" };
    else if (u.endsWith("/api/v1/admin/services")) body = { items: services };
    else if (u.includes("/facts") && init?.method === "POST") body = { ...facts[0], id: 2 };
    else if (u.includes("/facts")) body = { items: facts };
    else if (u.endsWith("/api/v1/admin/settings")) body = { overdraft_factor: "1.5", max_attempts: 5 };
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  };
  return new Api("", f as unknown as typeof fetch);
}

beforeEach(() => localStorage.clear());

test("departments list forms with their audience; only department facts are links", async () => {
  location.hash = "#/depts";
  render(<SessionProvider api={adminApi([])}><App /></SessionProvider>);
  expect(await screen.findByText("Сказки")).toBeTruthy();
  expect(screen.getByText("Расход").closest("a")?.getAttribute("href")).toBe("#/dept/taleweaver/taleweaver.spend.v1");
  expect(screen.getByText("Персонаж").closest("a")).toBeNull();
});

test("department memory shows the latest facts and writes one from the admin", async () => {
  const calls: string[] = [];
  location.hash = "#/dept/taleweaver/taleweaver.spend.v1";
  render(<SessionProvider api={adminApi(calls)}><App /></SessionProvider>);
  expect(await screen.findByText("prorab:prorab-1")).toBeTruthy();
  fireEvent.change(screen.getByLabelText(/model/), { target: { value: "gpt-z" } });
  fireEvent.click(screen.getByText("Записать"));
  expect(await screen.findByText("Записано.")).toBeTruthy();
  expect(calls.some((c) => c.startsWith("POST /api/v1/admin/services/taleweaver/facts") && c.includes('"model":"gpt-z"'))).toBe(true);
});

test("settings are shown read-only", async () => {
  location.hash = "#/settings";
  render(<SessionProvider api={adminApi([])}><App /></SessionProvider>);
  expect(await screen.findByText("overdraft_factor")).toBeTruthy();
  expect(screen.getByText("1.5")).toBeTruthy();
});
