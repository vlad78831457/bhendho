import { render, screen } from "@testing-library/react";

import { App } from "../../src/App";
import { Api } from "../../src/lib/api";
import { SessionProvider } from "../../src/session";

// Фронт движка открыт только админу (ADR-64); роль приходит из профиля (ADR-61).
function signedIn(role: string): Api {
  localStorage.setItem("offgrid.tokens", JSON.stringify({ access_token: "a", refresh_token: "r", expires_in: 60 }));
  const f = async (url: string | URL | Request) => {
    const u = String(url);
    const body = u.endsWith("/api/v1/me") ? { user_id: "u1", email: "a@b.io", role } : { items: [] };
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  };
  return new Api("", f as unknown as typeof fetch);
}

beforeEach(() => localStorage.clear());

test("a user without the admin role sees no panel", async () => {
  render(<SessionProvider api={signedIn("user")}><App /></SessionProvider>);
  expect(await screen.findByText("Нет доступа")).toBeTruthy();
  expect(screen.queryByText("Каталог")).toBeNull();
});

test("an admin sees the blank-driven front", async () => {
  render(<SessionProvider api={signedIn("admin")}><App /></SessionProvider>);
  expect(await screen.findByText("OFFGRID · админ")).toBeTruthy();
  expect(screen.queryByText("Нет доступа")).toBeNull();
});
