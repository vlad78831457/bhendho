import { Api, ApiError, parseOAuthHash } from "../../src/lib/api";

function json(status: number, body: unknown) {
  return new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

beforeEach(() => localStorage.clear());

test("refreshes an expired access token once and retries", async () => {
  const calls: string[] = [];
  let first = true;
  const f = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
    const u = String(url);
    calls.push(`${init?.method} ${u} ${(init?.headers as Record<string, string>)?.Authorization ?? ""}`);
    if (u.endsWith("/auth/login")) return json(200, { tokens: { access_token: "a1", refresh_token: "r1", expires_in: 60 } });
    if (u.endsWith("/auth/refresh")) return json(200, { tokens: { access_token: "a2", refresh_token: "r2", expires_in: 60 } });
    if (u.endsWith("/wallets") && first) { first = false; return json(401, {}); }
    return json(200, { items: [] });
  });
  const api = new Api("", f as unknown as typeof fetch);
  await api.login("a@b.io", "longpassword");
  expect(await api.wallets()).toEqual([]);
  expect(calls.filter((c) => c.includes("/wallets"))).toEqual([
    "GET /api/v1/wallets Bearer a1",
    "GET /api/v1/wallets Bearer a2",
  ]);
  expect(JSON.parse(localStorage.getItem("offgrid.tokens")!).refresh_token).toBe("r2");
});

test("failed refresh closes the session", async () => {
  const f = vi.fn(async (url: string | URL | Request) =>
    String(url).endsWith("/auth/login") ? json(200, { tokens: { access_token: "a", refresh_token: "r", expires_in: 1 } }) : json(401, {}));
  const api = new Api("", f as unknown as typeof fetch);
  let authed = true;
  api.onAuthChange = (a) => (authed = a);
  await api.login("a@b.io", "longpassword");
  await expect(api.wallets()).rejects.toBeInstanceOf(ApiError);
  expect(api.authed).toBe(false);
  expect(authed).toBe(false);
});

test("core refusals surface the registry code and field errors", async () => {
  const f = vi.fn(async () => json(422, { report: { verdict: "VALIDATION_ERROR", user_message: "Поле не прошло проверку",
    field_errors: [{ field: "start", message: "value must be at least 1" }] } }));
  const api = new Api("", f as unknown as typeof fetch);
  const err = await api.createTask("taleweaver.generate_start.v1", { start: 0 }, "k").catch((e: ApiError) => e);
  expect(err).toBeInstanceOf(ApiError);
  expect((err as ApiError).code).toBe("VALIDATION_ERROR");
  expect((err as ApiError).report?.field_errors?.[0].field).toBe("start");
});

test("task submission sends only form_id, values and idempotency_key (ADR-12)", async () => {
  const f = vi.fn(async () => json(201, { system: { document_id: "t" } }));
  await new Api("", f as unknown as typeof fetch).createTask("x.v1", { a: 1 }, "key-1");
  const body = JSON.parse((f.mock.calls[0] as unknown as [string, RequestInit])[1].body as string);
  expect(body).toEqual({ form_id: "x.v1", values: { a: 1 }, idempotency_key: "key-1" });
});

test("reads the sign-in result the provider returned in the page address", () => {
  expect(parseOAuthHash("#oauth=abc-_1")).toEqual({ code: "abc-_1" });
  expect(parseOAuthHash("#oauth_error=denied")).toEqual({ error: "denied" });
  expect(parseOAuthHash("")).toBeNull();
  expect(parseOAuthHash("#desk")).toBeNull();
  expect(parseOAuthHash("#oauth_linked=google")).toEqual({ linked: "google" });
  expect(parseOAuthHash("#oauth_link_error=taken")).toEqual({ linkError: "taken" });
});

test("exchanges a one-time sign-in code for a session", async () => {
  const f = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
    expect(String(url)).toBe("/api/v1/auth/oauth/exchange");
    expect(JSON.parse(String(init?.body))).toEqual({ code: "c1" });
    return json(200, { user_id: "u", tokens: { access_token: "a", refresh_token: "r", expires_in: 60 } });
  });
  const api = new Api("", f as unknown as typeof fetch);
  await api.oauthExchange("c1");
  expect(api.authed).toBe(true);
  expect(api.oauthStartUrl("vk", "/?x=1")).toBe("/api/v1/auth/oauth/vk/start?return_to=%2F%3Fx%3D1");
});

test("uploads an image as the raw request body with its type", async () => {
  const f = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
    expect(String(url)).toBe("/api/v1/files?filename=%D1%81%D1%8B%D0%BD.jpg");
    expect((init?.headers as Record<string, string>)["Content-Type"]).toBe("image/jpeg");
    expect(init?.body).toBeInstanceOf(Blob);
    return json(201, { file: { file_id: "f1" }, link: { url: "/c", expires_at: "2030-01-01T00:00:00Z" } });
  });
  const api = new Api("", f as unknown as typeof fetch);
  const r = await api.uploadImage(new Blob(["jpeg"], { type: "image/jpeg" }), "сын.jpg");
  expect(r.file.file_id).toBe("f1");
});
