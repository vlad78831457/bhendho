// Клиент потоков 5.7: вниз — каталог, шаблоны, кошелёк, история; вверх — form_id + values +
// idempotency_key, команды, авторизация. Цену и схему фронт не отправляет (ADR-12).
// Запросы — один клиент API людей из SDK ядра (ADR-81): адреса и типы ответов — из договора (ADR-80).
// Здесь — сессия панели (ключи в offgrid.tokens, одно обновление на всех) и привычные методы страниц.

import { People, PeopleError, type CallOptions, type Method, type paths, type Reply } from "../offgrid";
import { asEnvelope, asTemplate } from "./types";
import type {
  AdminService, CatalogItem, DepartmentKey, Envelope, Fact, FileLink, Identity, Profile, Prorab, Report, ServiceFact,
  ShowcaseItem, StoredFile, Tariffs, Template, Tokens, Transaction, Wallet,
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
  private people: People;
  onAuthChange: (authed: boolean) => void = () => {};

  constructor(private base = "", private fetchImpl: typeof fetch = (...a) => fetch(...a)) {
    this.people = new People({
      base: () => this.base,
      token: () => this.tokens?.access_token ?? null,
      refresh: () => this.refresh(),
      fetch: (...a) => this.fetchImpl(...a),
    });
  }

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

  /** Адрес API людей по договору; отказ ядра — ApiError, как раньше видели страницы. */
  private async call<P extends keyof paths, M extends Method & keyof paths[P]>(
    method: M,
    path: P,
    opts: CallOptions<P, M> = {},
  ): Promise<Reply<P, M>> {
    try {
      return await this.people.call(method, path, opts);
    } catch (err) {
      throw asApiError(err);
    }
  }

  /** Файл по адресу ядра с ключом (выгрузка): 401 — одна попытка обновить ключ. */
  private async download<P extends keyof paths>(path: P, opts: CallOptions<P, "get">): Promise<Blob> {
    try {
      return await (await this.people.raw("get" as Method & keyof paths[P], path, opts as never)).blob();
    } catch (err) {
      throw asApiError(err);
    }
  }

  /** Ротация refresh-токена; при отказе сессия закрывается (ADR-44). */
  private refresh(): Promise<boolean> {
    if (!this.refreshing) {
      const token = this.tokens?.refresh_token;
      this.refreshing = (async () => {
        try {
          const r = await this.people.call("post", "/api/v1/auth/refresh", { body: { refresh_token: token ?? "" }, noRefresh: true });
          this.setTokens(r.tokens);
          return true;
        } catch (err) {
          // Сессию закрывает только отказ ядра; сеть пропала — токены остаются до следующей попытки (разбор 6.1).
          if (err instanceof PeopleError && (err.status === 401 || err.status === 403)) this.setTokens(null);
          return false;
        } finally {
          this.refreshing = null;
        }
      })();
    }
    return this.refreshing;
  }

  async register(email: string, password: string, displayName = ""): Promise<void> {
    const r = await this.call("post", "/api/v1/auth/register", { body: { email, password, display_name: displayName }, noRefresh: true });
    this.setTokens(r.tokens);
  }

  async login(email: string, password: string): Promise<void> {
    const r = await this.call("post", "/api/v1/auth/login", { body: { email, password }, noRefresh: true });
    this.setTokens(r.tokens);
  }

  /** Вход через внешние аккаунты (ADR-58): включённые провайдеры — для кнопок входа. */
  authProviders = () => this.call("get", "/api/v1/auth/providers", { noRefresh: true }).then((r) => r.providers);

  /** Куда уйти браузеру: провайдер вернёт на returnTo с #oauth=<код> или #oauth_error=<причина>. */
  oauthStartUrl = (provider: string, returnTo: string) =>
    `${this.base}/api/v1/auth/oauth/${encodeURIComponent(provider)}/start?return_to=${encodeURIComponent(returnTo)}`;

  /** Одноразовый код из #oauth=… → сессия. */
  async oauthExchange(code: string): Promise<void> {
    const r = await this.call("post", "/api/v1/auth/oauth/exchange", { body: { code }, noRefresh: true });
    this.setTokens(r.tokens);
  }

  async logout(): Promise<void> {
    const t = this.tokens;
    this.setTokens(null);
    if (t) await this.call("post", "/api/v1/auth/logout", { body: { refresh_token: t.refresh_token }, noRefresh: true }).catch(() => {});
  }

  me = (): Promise<Profile> => this.call("get", "/api/v1/me");

  // --- кабинет ---
  updateMe = (displayName: string): Promise<Profile> => this.call("patch", "/api/v1/me", { body: { display_name: displayName } });
  changePassword = (current: string, next: string): Promise<void> =>
    this.call("post", "/api/v1/me/password", { body: { current_password: current, new_password: next } });
  identities = (): Promise<Identity[]> => this.call("get", "/api/v1/me/identities").then((r) => r.items);
  /** Привязать ещё один вход: адрес провайдера; вернёт на returnTo с #oauth_linked=… или #oauth_link_error=…. */
  linkIdentity = (provider: string, returnTo: string): Promise<string> =>
    this.call("post", "/api/v1/me/identities/{provider}/link", { path: { provider }, body: { return_to: returnTo } }).then((r) => r.url);
  unlinkIdentity = (provider: string, subject: string): Promise<void> =>
    this.call("delete", "/api/v1/me/identities/{provider}/{subject}", { path: { provider, subject } });
  features = (): Promise<Record<string, boolean>> => this.call("get", "/api/v1/features", { noRefresh: true });
  /** Карточки фактов для списков: без удалённых, с выбранными полями опубликованной версии в summary. */
  factCards = (kind: string, fields: string[]): Promise<Fact[]> =>
    this.call("get", "/api/v1/facts", { query: { kind, fields: fields.join(","), live: "true" } }).then((r) => r.items as Fact[]);
  deleteFact = (id: string): Promise<void> => this.call("delete", "/api/v1/facts/{id}", { path: { id } });

  // --- файлы (ADR-60) ---
  /** Картинка: ядро пересохраняет её в JPEG (поворот по камере, без метаданных, до 1024 px). */
  uploadImage = (file: Blob, filename: string): Promise<{ file: StoredFile; link: FileLink }> =>
    this.call("post", "/api/v1/files", { query: { filename }, body: file });
  /** Подписанная ссылка для <img> (живёт минуты) — заново при каждом показе. */
  fileLink = (id: string): Promise<FileLink> => this.call("get", "/api/v1/files/{id}", { path: { id } }).then((r) => r.link);
  deleteFile = (id: string): Promise<void> => this.call("delete", "/api/v1/files/{id}", { path: { id } });
  catalog = (): Promise<CatalogItem[]> => this.call("get", "/api/v1/catalog").then((r) => r.items);
  template = (formId: string): Promise<Template> =>
    this.call("get", "/api/v1/forms/{form_id}", { path: { form_id: formId } }).then(asTemplate);
  wallets = (): Promise<Wallet[]> => this.call("get", "/api/v1/wallets").then((r) => r.items);
  transactions = (): Promise<Transaction[]> =>
    this.call("get", "/api/v1/wallets/transactions", { query: { limit: 50 } }).then((r) => r.items);
  demoDeposit = (amount: number): Promise<Wallet> => this.call("post", "/api/v1/deposits/demo", { body: { amount: String(amount) } });

  // Панель админа (ADR-66), только роль admin.
  adminServices = (): Promise<AdminService[]> => this.call("get", "/api/v1/admin/services").then((r) => r.items as AdminService[]);
  adminFacts = (service: string, formId: string, limit = 100): Promise<ServiceFact[]> =>
    this.call("get", "/api/v1/admin/services/{service}/facts", { path: { service }, query: { form_id: formId, limit } }).then((r) => r.items);
  adminWriteFact = (service: string, formId: string, values: Record<string, unknown>): Promise<ServiceFact> =>
    this.call("post", "/api/v1/admin/services/{service}/facts", { path: { service }, body: { form_id: formId, values } });
  adminSettings = (): Promise<Record<string, unknown>> => this.call("get", "/api/v1/admin/settings");
  adminTariffs = (): Promise<Tariffs> => this.call("get", "/api/v1/admin/tariffs");
  /** Уровни прорабов (ADR-76): приоритет прораба на бланк, 0 — не берёт. */
  adminProrabs = (service: string): Promise<Prorab[]> =>
    this.call("get", "/api/v1/admin/services/{service}/prorabs", { path: { service } }).then((r) => r.items);
  adminSetProrab = (service: string, prorab: string, formId: string, priority: number): Promise<Prorab[]> =>
    this.call("put", "/api/v1/admin/services/{service}/prorabs", { path: { service }, body: { prorab, form_id: formId, priority } })
      .then((r) => r.items);
  // Витрина (ADR-74).
  adminShowcase = (): Promise<ShowcaseItem[]> => this.call("get", "/api/v1/admin/showcase").then((r) => r.items);
  adminAddShowcase = (factId: string, language: string, position: number): Promise<ShowcaseItem> =>
    this.call("post", "/api/v1/admin/showcase", { body: { fact_id: factId, language, position } });
  adminRemoveShowcase = (id: string): Promise<void> => this.call("delete", "/api/v1/admin/showcase/{id}", { path: { id } });
  /** Видимость своего факта (ADR-79): публичный — на него ссылается любой. */
  adminSetVisibility = (factId: string, visibility: "public" | "private"): Promise<Fact> =>
    this.call("put", "/api/v1/admin/facts/{id}/visibility", { path: { id: factId }, body: { visibility } });
  /** Ключ подразделения (ADR-70): показывается один раз. */
  adminDepartmentKeys = (): Promise<DepartmentKey[]> => this.call("get", "/api/v1/admin/departments/keys").then((r) => r.items);
  adminDepartmentKey = (code: string, name: string): Promise<{ code: string; name: string; key: string }> =>
    this.call("post", "/api/v1/admin/departments/keys", { body: { code, name } });
  adminSetEnabled = (service: string, enabled: boolean): Promise<void> =>
    this.call("put", "/api/v1/admin/services/{service}", { path: { service }, body: { enabled } });
  /** value null — вернуть как в паспорте или настройках. */
  adminSetTariff = (scope: string, target: string, value: string | null): Promise<Tariffs> =>
    this.call("put", "/api/v1/admin/tariffs", { body: { scope, target, value } });
  /** Вся база за период архивом (ADR-67); to не включается. */
  adminExport = (from: string, to: string): Promise<Blob> => this.download("/api/v1/admin/export", { query: { from, to } });
  /** Промокод службы оплаты (ADR-57): зачисление один раз; повтор того же пользователя — already_redeemed. */
  redeemPromo = (code: string) => this.call("post", "/api/v1/promo-codes/redeem", { body: { code } });

  facts = (kind = ""): Promise<Fact[]> =>
    this.call("get", "/api/v1/facts", { query: kind ? { kind } : {} }).then((r) => r.items as Fact[]);
  fact = (id: string): Promise<Fact> => this.call("get", "/api/v1/facts/{id}", { path: { id } });
  createFact = (formId: string, values: Record<string, unknown>): Promise<Fact> =>
    this.call("post", "/api/v1/facts", { body: { form_id: formId, values } });
  publishFact = (id: string, version = 0): Promise<Fact> =>
    this.call("post", "/api/v1/facts/{id}/publish", { path: { id }, body: version ? { version } : undefined });

  createTask = (formId: string, values: Record<string, unknown>, idempotencyKey: string, groupId?: string): Promise<Envelope> =>
    this.call("post", "/api/v1/tasks", {
      body: { form_id: formId, values, idempotency_key: idempotencyKey, ...(groupId ? { group_id: groupId } : {}) },
    }).then(asEnvelope);
  tasks = (status = "", limit = 20, offset = 0): Promise<{ items: Envelope[]; total: number }> =>
    this.call("get", "/api/v1/tasks", { query: { limit, offset, ...(status ? { status } : {}) } }).then((r) => ({
      items: r.items.map(asEnvelope),
      total: r.total,
    }));
  task = (id: string): Promise<Envelope> => this.call("get", "/api/v1/tasks/{id}", { path: { id } }).then(asEnvelope);
  /** Команда задачи: имена — из её commands (ядро говорит, какие доступны); договор знает cancel и retry. */
  command = (id: string, command: string, idempotencyKey?: string): Promise<Envelope> => {
    if (command !== "cancel" && command !== "retry") {
      return Promise.reject(new ApiError(422, null, "invalid_input", `unknown command ${command}`));
    }
    return this.call("post", "/api/v1/tasks/{id}/commands", {
      path: { id }, body: { command, ...(idempotencyKey ? { idempotency_key: idempotencyKey } : {}) },
    }).then(asEnvelope);
  };

  /** «Проверить сеть»: ядро живо и отвечает (FR-OFF-4). */
  async ping(): Promise<boolean> {
    try {
      return (await this.fetchImpl(`${this.base}/healthz`, { cache: "no-store" })).ok;
    } catch {
      return false;
    }
  }
}

function asApiError(err: unknown): unknown {
  if (!(err instanceof PeopleError)) return err;
  return new ApiError(err.status, (err.report as Report | undefined) ?? null, err.code, err.message);
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
