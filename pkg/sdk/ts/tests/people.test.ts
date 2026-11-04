// Клиент API людей (ADR-81): путь и строка запроса по договору, ключ входа, одно обновление ключа при 401,
// отказ ядра — PeopleError с кодом реестра.
import { People, PeopleError, type Schemas } from "../src";

type Seen = { url: string; method: string; auth: string | null; body: string | null };

function fake(replies: Response[]) {
  const seen: Seen[] = [];
  const f = (async (url: string, init: RequestInit) => {
    const h = new Headers(init.headers);
    seen.push({ url, method: String(init.method), auth: h.get("Authorization"), body: (init.body as string) ?? null });
    return replies.shift() ?? new Response("{}", { status: 500 });
  }) as unknown as typeof fetch;
  return { seen, f };
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

test("путь, строка запроса, тело и ключ — по договору; ответ типизирован схемой", async () => {
  const fact: Schemas["Fact"] = { fact_id: "f1", kind: "taleweaver.tale.v2", origin: "user", visibility: "private", versions: [] };
  const { seen, f } = fake([json(200, fact), json(200, { items: [] }), json(201, { ...fact, fact_id: "f2" })]);
  const p = new People({ base: () => "https://site", token: () => "tok", fetch: f });
  const got = await p.call("get", "/api/v1/facts/{id}", { path: { id: "a/b" } });
  expect(got.fact_id).toBe("f1");
  await p.call("get", "/api/v1/facts", { query: { kind: "taleweaver.tale.v2", fields: "title", live: undefined } });
  const made = await p.call("post", "/api/v1/facts", { body: { form_id: "x", values: { title: "T" } } });
  expect(made.fact_id).toBe("f2");
  expect(seen.map((s) => `${s.method} ${s.url}`)).toEqual([
    "GET https://site/api/v1/facts/a%2Fb",
    "GET https://site/api/v1/facts?kind=taleweaver.tale.v2&fields=title",
    "POST https://site/api/v1/facts",
  ]);
  expect(seen[0].auth).toBe("Bearer tok");
  expect(JSON.parse(seen[2].body!)).toEqual({ form_id: "x", values: { title: "T" } });
});

test("401 — одно обновление ключа и повтор; 204 — undefined", async () => {
  let token = "old";
  const { seen, f } = fake([json(401, { error: { code: "unauthorized", message: "x" } }), new Response(null, { status: 204 })]);
  const p = new People({ token: () => token, refresh: async () => ((token = "new"), true), fetch: f });
  expect(await p.call("post", "/api/v1/me/password", { body: { new_password: "secret-secret" } })).toBeUndefined();
  expect(seen.map((s) => s.auth)).toEqual(["Bearer old", "Bearer new"]);
});

test("отказ ядра — PeopleError: код реестра из report, иначе error.code", async () => {
  const { f } = fake([
    json(402, { report: { verdict: "INSUFFICIENT_FUNDS", user_message: "Не хватает GC" } }),
    json(409, { error: { code: "email_taken", message: "email already registered" } }),
  ]);
  const p = new People({ fetch: f });
  await expect(p.call("post", "/api/v1/tasks", { body: { form_id: "x", values: {} } })).rejects.toMatchObject({
    status: 402, code: "INSUFFICIENT_FUNDS", message: "Не хватает GC",
  });
  const err = await p.call("post", "/api/v1/auth/register", { body: { email: "a@b", password: "p" } }).catch((e) => e);
  expect(err).toBeInstanceOf(PeopleError);
  expect([err.status, err.code]).toEqual([409, "email_taken"]);
});

test("файл — сырым телом со своим типом", async () => {
  const { seen, f } = fake([json(201, { file: { file_id: "f1", filename: "a.jpg", mime: "image/jpeg", size: 1, created_at: "" },
    link: { url: "/c", expires_at: "" } })]);
  const types: (string | null)[] = [];
  const spy = (async (url: string, init: RequestInit) => {
    types.push(new Headers(init.headers).get("Content-Type"));
    return f(url, init);
  }) as unknown as typeof fetch;
  const r = await new People({ fetch: spy }).call("post", "/api/v1/files", { query: { filename: "сын.jpg" }, body: new Blob(["x"], { type: "image/jpeg" }) });
  expect(r.file.file_id).toBe("f1");
  expect(types).toEqual(["image/jpeg"]);
  expect(seen[0].url).toBe("/api/v1/files?filename=%D1%81%D1%8B%D0%BD.jpg");
});
