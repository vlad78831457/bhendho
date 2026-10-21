import { fireEvent, render, screen } from "@testing-library/react";

import { App } from "../../src/App";
import { Api } from "../../src/lib/api";
import { SessionProvider } from "../../src/session";

// Простая панель админа (ADR-66): подразделения, память подразделения, настройки.
const services = [{
  code: "taleweaver", title: "Сказки", module_id: "taleweaver/story-engine", module_version: "0.1.0", enabled: true,
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
    else if (u.includes("/prorabs")) body = { items: [] };
    else if (u.endsWith("/api/v1/admin/departments/keys")) body = init?.method === "POST"
      ? { code: "echo", name: "main", key: "dk_123" }
      : { items: [{ code: "echo", name: "main", created_at: "2026-10-16T00:00:00Z", connected: false, title: "", last_seen_at: null }] };
    else if (u.endsWith("/api/v1/admin/services/taleweaver")) return new Response(null, { status: 204 });
    else if (u.endsWith("/api/v1/admin/tariffs")) body = {
      forms: [{ service: "taleweaver", form_id: "taleweaver.generate_start.v1", title: "Сказка", manifest_price: "150.0000", price: "150.0000", override: false }],
      units: [{ service: "taleweaver", unit: "images", base: false, manifest_rate: "20000", rate: "20000", override: false }],
      overdraft_factor: { config: "1.5", value: "1.5", override: false },
      multipliers: [{ target: "core", title: "Ядро — k GC", value: "1", override: false }, { target: "taleweaver:middle", title: "Среднички", value: "1", override: false }],
      history: [],
    };
    else if (u.includes("/api/v1/admin/export")) return new Response("PK", { status: 200, headers: { "Content-Type": "application/zip" } });
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

test("export downloads the whole database for the chosen period", async () => {
  const calls: string[] = [];
  URL.createObjectURL = vi.fn(() => "blob:x");
  URL.revokeObjectURL = vi.fn();
  const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  location.hash = "#/depts";
  render(<SessionProvider api={adminApi(calls)}><App /></SessionProvider>);
  await screen.findByText("Сказки");
  fireEvent.change(screen.getByLabelText(/^С/), { target: { value: "2026-10-01" } });
  fireEvent.change(screen.getByLabelText(/по/), { target: { value: "2026-10-16" } });
  fireEvent.click(screen.getByText("Выгрузить"));
  expect(await screen.findByText("Архив скачан.")).toBeTruthy();
  expect(calls).toContain("GET /api/v1/admin/export?from=2026-10-01&to=2026-10-16 ");
  expect(click).toHaveBeenCalled();
});

test("tariffs: a new price is sent and applies to new tasks", async () => {
  const calls: string[] = [];
  location.hash = "#/tariffs";
  render(<SessionProvider api={adminApi(calls)}><App /></SessionProvider>);
  fireEvent.change(await screen.findByLabelText("новое: Сказка (taleweaver)"), { target: { value: "199,5" } });
  fireEvent.click(screen.getAllByText("Сохранить")[0]);
  expect(await screen.findByText("Сохранено. Действует для новых задач.")).toBeTruthy();
  expect(calls).toContain('PUT /api/v1/admin/tariffs {"scope":"form_price","target":"taleweaver.generate_start.v1","value":"199.5"}');
});

test("tariffs: a multiplier for a prorab is sent", async () => {
  const calls: string[] = [];
  location.hash = "#/tariffs";
  render(<SessionProvider api={adminApi(calls)}><App /></SessionProvider>);
  fireEvent.change(await screen.findByLabelText("новое: taleweaver:middle — Среднички"), { target: { value: "1,2" } });
  const row = screen.getByLabelText("новое: taleweaver:middle — Среднички").closest("tr");
  fireEvent.click(row!.querySelector("button")!);
  expect(await screen.findByText("Сохранено. Действует для новых задач.")).toBeTruthy();
  expect(calls).toContain('PUT /api/v1/admin/tariffs {"scope":"multiplier","target":"taleweaver:middle","value":"1.2"}');
});

test("departments: a key is issued once and a department is switched off", async () => {
  const calls: string[] = [];
  location.hash = "#/depts";
  render(<SessionProvider api={adminApi(calls)}><App /></SessionProvider>);
  await screen.findByText("Сказки");
  fireEvent.change(screen.getByLabelText(/Код/), { target: { value: "echo" } });
  fireEvent.click(screen.getByText("Выдать ключ"));
  expect(await screen.findByText(/dk_123/)).toBeTruthy();
  fireEvent.click(screen.getByText("Выключить"));
  await screen.findByText("Сказки");
  expect(calls).toContain('PUT /api/v1/admin/services/taleweaver {"enabled":false}');
});
