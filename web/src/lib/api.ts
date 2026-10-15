// Клиент потоков 5.7: вниз — каталог, шаблоны, кошелёк, история; вверх — form_id + values +
// idempotency_key, команды, авторизация. Цену и схему фронт не отправляет (ADR-12).

import type {
  AdminService, CatalogItem, Envelope, Fact, FileLink, Identity, Profile, Report, ServiceFact, StoredFile, Template, Tokens,
  Transaction, Wallet,
} from "./types";

const TOKENS_KEY = "offgrid.tokens";

/** Отказ ядра с report (код реестра 5.10) или HTTP-ошибка. */
export class ApiError extends Error {
  constructor(
    public status: number,
    public report: Report | null,
    public code: string,
    message: string,
  ) {
    super(message);
  }
}

function load(): Tokens | null {
  try {
    const raw = localStorage.getItem(TOKENS_KEY);
    return raw ? (JSON.parse(raw) as Tokens) : null;
  } catch {
    return null;
  }
}

function save(t: Tokens | null): void {
  try {
    if (t) localStorage.setItem(TOKENS_KEY, JSON.stringify(t));
    else localStorage.removeItem(TOKENS_KEY);
  } catch {
    /* приватный режим — сессия живёт в памяти */
  }
}

export class Api {
  private tokens: Tokens | null = load();
  private refreshing: Promise<boolean> | null = null;
  onAuthChange: (authed: boolean) => void = () => {};

  constructor(private base = "", private fetchImpl: typeof fetch = (...a) => fetch(...a)) {}

  get authed(): boolean {
    return this.tokens !== null;
  }

  get accessToken(): string | null {
    return this.tokens?.access_token ?? null;
  }

  private setTokens(t: Tokens | null): void {
    this.tokens = t;
    save(t);
    this.onAuthChange(t !== null);
  }

  private request<T>(method: string, path: string, body?: unknown, retry = true): Promise<T> {
    return body === undefined
      ? this.send<T>(method, path, undefined, "", retry)
      : this.send<T>(method, path, JSON.stringify(body), "application/json", retry);
  }

  /** Запрос с телом как есть (JSON или байты файла); 401 — одна попытка обновить токен. */
  private async send<T>(method: string, path: string, body: BodyInit | undefined, contentType: string, retry = true): Promise<T> {
    const headers: Record<string, string> = {};
    if (contentType) headers["Content-Type"] = contentType;
    if (this.tokens) headers.Authorization = `Bearer ${this.tokens.access_token}`;
    const res = await this.fetchImpl(this.base + path, { method, headers, body });
    if (res.status === 401 && retry && this.tokens && (await this.refresh())) {
      return this.send<T>(method, path, body, contentType, false);
    }
    if (res.status === 204) return undefined as T;
    const data = await res.json().catch(() => ({}));
    if (!res.ok) {
      const report = (data?.report as Report | undefined) ?? null;
      const code = report?.verdict ?? data?.error?.code ?? `HTTP_${res.status}`;
      const message = report?.user_message ?? data?.error?.message ?? res.statusText;
      throw new ApiError(res.status, report, code, message);
    }
    return data as T;
  }

  /** Файл по адресу ядра с токеном (выгрузка): 401 — одна попытка обновить токен. */
  async download(path: string, retry = true): Promise<Blob> {
    const headers: Record<string, string> = {};
    if (this.tokens) headers.Authorization = `Bearer ${this.tokens.access_token}`;
    const res = await this.fetchImpl(this.base + path, { method: "GET", headers });
    if (res.status === 401 && retry && this.tokens && (await this.refresh())) return this.download(path, false);
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      throw new ApiError(res.status, null, data?.error?.code ?? `HTTP_${res.status}`, data?.error?.message ?? res.statusText);
    }
    return res.blob();
  }

  /** Ротация refresh-токена; при отказе сессия закрывается (ADR-44). */
  private refresh(): Promise<boolean> {
    if (!this.refreshing) {
      const token = this.tokens?.refresh_token;
      this.refreshing = (async () => {
        try {
          const res = await this.fetchImpl(`${this.base}/api/v1/auth/refresh`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ refresh_token: token }),
          });
          if (!res.ok) throw new Error("refresh failed");
          this.setTokens(((await res.json()) as { tokens: Tokens }).tokens);
          return true;
        } catch {
          this.setTokens(null);
          return false;
        } finally {
          this.refreshing = null;
        }
      })();
    }
    return this.refreshing;
  }

  async register(email: string, password: string, displayName = ""): Promise<void> {
    const r = await this.request<{ tokens: Tokens }>("POST", "/api/v1/auth/register", {
      email, password, display_name: displayName,
    }, false);
    this.setTokens(r.tokens);
  }

  async login(email: string, password: string): Promise<void> {
    const r = await this.request<{ tokens: Tokens }>("POST", "/api/v1/auth/login", { email, password }, false);
    this.setTokens(r.tokens);
  }

  /** Вход через внешние аккаунты (ADR-58): включённые провайдеры — для кнопок входа. */
  authProviders = () =>
    this.request<{ providers: AuthProvider[] }>("GET", "/api/v1/auth/providers", undefined, false).then((r) => r.providers);

  /** Куда уйти браузеру: провайдер вернёт на returnTo с #oauth=<код> или #oauth_error=<причина>. */
  oauthStartUrl = (provider: string, returnTo: string) =>
    `${this.base}/api/v1/auth/oauth/${encodeURIComponent(provider)}/start?return_to=${encodeURIComponent(returnTo)}`;

  /** Одноразовый код из #oauth=… → сессия. */
  async oauthExchange(code: string): Promise<void> {
    const r = await this.request<{ tokens: Tokens }>("POST", "/api/v1/auth/oauth/exchange", { code }, false);
    this.setTokens(r.tokens);
  }

  async logout(): Promise<void> {
    const t = this.tokens;
    this.setTokens(null);
    if (t) await this.request("POST", "/api/v1/auth/logout", { refresh_token: t.refresh_token }, false).catch(() => {});
  }

  me = () => this.request<Profile>("GET", "/api/v1/me");

  // --- кабинет ---
  updateMe = (displayName: string) => this.request<Profile>("PATCH", "/api/v1/me", { display_name: displayName });
  changePassword = (current: string, next: string) =>
    this.request<void>("POST", "/api/v1/me/password", { current_password: current, new_password: next });
  identities = () => this.request<{ items: Identity[] }>("GET", "/api/v1/me/identities").then((r) => r.items);
  /** Привязать ещё один вход: адрес провайдера; вернёт на returnTo с #oauth_linked=… или #oauth_link_error=…. */
  linkIdentity = (provider: string, returnTo: string) =>
    this.request<{ url: string }>("POST", `/api/v1/me/identities/${encodeURIComponent(provider)}/link`, { return_to: returnTo })
      .then((r) => r.url);
  unlinkIdentity = (provider: string, subject: string) =>
    this.request<void>("DELETE", `/api/v1/me/identities/${encodeURIComponent(provider)}/${encodeURIComponent(subject)}`);
  features = () => this.request<Record<string, boolean>>("GET", "/api/v1/features", undefined, false);
  /** Карточки фактов для списков: без удалённых, с выбранными полями опубликованной версии в summary. */
  factCards = (kind: string, fields: string[]) =>
    this.request<{ items: Fact[] }>(
      "GET", `/api/v1/facts?kind=${encodeURIComponent(kind)}&fields=${encodeURIComponent(fields.join(","))}&live=true`,
    ).then((r) => r.items);
  deleteFact = (id: string) => this.request<void>("DELETE", `/api/v1/facts/${id}`);

  // --- файлы (ADR-60) ---
  /** Картинка: ядро пересохраняет её в JPEG (поворот по камере, без метаданных, до 1024 px). */
  uploadImage = (file: Blob, filename: string) =>
    this.send<{ file: StoredFile; link: FileLink }>(
      "POST", `/api/v1/files?filename=${encodeURIComponent(filename)}`, file, file.type || "application/octet-stream",
    );
  /** Подписанная ссылка для <img> (живёт минуты) — заново при каждом показе. */
  fileLink = (id: string) =>
    this.request<{ file: StoredFile; link: FileLink }>("GET", `/api/v1/files/${id}`).then((r) => r.link);
  deleteFile = (id: string) => this.request<void>("DELETE", `/api/v1/files/${id}`);
  catalog = () => this.request<{ items: CatalogItem[] }>("GET", "/api/v1/catalog").then((r) => r.items);
  template = (formId: string) => this.request<Template>("GET", `/api/v1/forms/${encodeURIComponent(formId)}`);
  wallets = () => this.request<{ items: Wallet[] }>("GET", "/api/v1/wallets").then((r) => r.items);
  transactions = () =>
    this.request<{ items: Transaction[] }>("GET", "/api/v1/wallets/transactions?limit=50").then((r) => r.items);
  demoDeposit = (amount: number) => this.request<Wallet>("POST", "/api/v1/deposits/demo", { amount });

  // Панель админа (ADR-66), только роль admin.
  adminServices = () => this.request<{ items: AdminService[] }>("GET", "/api/v1/admin/services").then((r) => r.items);
  adminFacts = (service: string, formId: string, limit = 100) =>
    this.request<{ items: ServiceFact[] }>(
      "GET",
      `/api/v1/admin/services/${encodeURIComponent(service)}/facts?form_id=${encodeURIComponent(formId)}&limit=${limit}`,
    ).then((r) => r.items);
  adminWriteFact = (service: string, formId: string, values: Record<string, unknown>) =>
    this.request<ServiceFact>("POST", `/api/v1/admin/services/${encodeURIComponent(service)}/facts`, { form_id: formId, values });
  adminSettings = () => this.request<Record<string, unknown>>("GET", "/api/v1/admin/settings");
  /** Вся база за период архивом (ADR-67); to не включается. */
  adminExport = (from: string, to: string) =>
    this.download(`/api/v1/admin/export?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`);
  /** Промокод службы оплаты (ADR-57): зачисление один раз; повтор того же пользователя — already_redeemed. */
  redeemPromo = (code: string) =>
    this.request<{ code_id: string; amount: string; currency_id: string; already_redeemed: boolean; wallet: Wallet }>(
      "POST", "/api/v1/promo-codes/redeem", { code },
    );

  facts = (kind = "") =>
    this.request<{ items: Fact[] }>("GET", `/api/v1/facts${kind ? `?kind=${encodeURIComponent(kind)}` : ""}`).then(
      (r) => r.items,
    );
  fact = (id: string) => this.request<Fact>("GET", `/api/v1/facts/${id}`);
  createFact = (formId: string, values: Record<string, unknown>) =>
    this.request<Fact>("POST", "/api/v1/facts", { form_id: formId, values });
  publishFact = (id: string, version = 0) =>
    this.request<Fact>("POST", `/api/v1/facts/${id}/publish`, version ? { version } : undefined);

  createTask = (formId: string, values: Record<string, unknown>, idempotencyKey: string, groupId?: string) =>
    this.request<Envelope>("POST", "/api/v1/tasks", {
      form_id: formId, values, idempotency_key: idempotencyKey, ...(groupId ? { group_id: groupId } : {}),
    });
  tasks = (status = "", limit = 20, offset = 0) =>
    this.request<{ items: Envelope[]; total: number }>(
      "GET", `/api/v1/tasks?limit=${limit}&offset=${offset}${status ? `&status=${status}` : ""}`,
    );
  task = (id: string) => this.request<Envelope>("GET", `/api/v1/tasks/${id}`);
  command = (id: string, command: string, idempotencyKey?: string) =>
    this.request<Envelope>("POST", `/api/v1/tasks/${id}/commands`, {
      command, ...(idempotencyKey ? { idempotency_key: idempotencyKey } : {}),
    });

  /** «Проверить сеть»: ядро живо и отвечает (FR-OFF-4). */
  async ping(): Promise<boolean> {
    try {
      return (await this.fetchImpl(`${this.base}/healthz`, { cache: "no-store" })).ok;
    } catch {
      return false;
    }
  }
}

/** Провайдер входа (Google, VK ID…). */
export interface AuthProvider {
  id: string;
  name: string;
}

/** Итог возврата от провайдера в адресе страницы: вход (#oauth=<код> | #oauth_error=<причина>)
 *  или привязка из кабинета (#oauth_linked=<провайдер> | #oauth_link_error=<причина>). */
export type OAuthResult = { code: string } | { error: string } | { linked: string } | { linkError: string };

export function parseOAuthHash(hash: string): OAuthResult | null {
  const p = new URLSearchParams(hash.replace(/^#/, ""));
  const code = p.get("oauth");
  if (code) return { code };
  const error = p.get("oauth_error");
  if (error) return { error };
  const linked = p.get("oauth_linked");
  if (linked) return { linked };
  const linkError = p.get("oauth_link_error");
  return linkError ? { linkError } : null;
}
