// Клиент API людей (ADR-81) — один на экран подразделения, лик и панель вместо ручных копий. Типы — из договора
// contracts/interfaces/people.openapi.yaml (people.gen.ts собирается из него; сборка проверяет, что он свежий):
// переименовали поле в ядре — у экрана не соберётся код, а не сломается страница у человека.
// Без зависимостей во время работы: экраны берут исходники SDK к себе (ADR-72), нужен только fetch.

import type { components, paths } from "./people.gen";

export type { components, paths } from "./people.gen";
/** Схемы договора: Schemas["Profile"], Schemas["Wallet"], Schemas["Task"]… */
export type Schemas = components["schemas"];
export type PeoplePath = keyof paths;
export type Method = "get" | "post" | "put" | "patch" | "delete";

type Op<P extends PeoplePath, M extends Method> = NonNullable<paths[P][M]>;
type Responses<O> = O extends { responses: infer R } ? R : never;
type Success<R> = R[Extract<keyof R, 200 | 201 | 202 | 204>];
type JSONOf<X> = X extends { content: { "application/json": infer B } } ? B : undefined;
/** Тело успешного ответа адреса: 200/201 — JSON по схеме, 204 — undefined. */
export type Reply<P extends PeoplePath, M extends Method> = JSONOf<Success<Responses<Op<P, M>>>>;

type BodyOf<O> = O extends { requestBody?: { content: { "application/json": infer B } } } ? B : never;
type ParamsOf<O, K extends "path" | "query"> = O extends { parameters: { [k in K]?: infer X } }
  ? [X] extends [never | undefined]
    ? never
    : X
  : never;

/** Что передать в адрес: части пути ({id}), строку запроса и тело — по договору. */
export interface CallOptions<P extends PeoplePath, M extends Method> {
  path?: ParamsOf<Op<P, M>, "path">;
  query?: ParamsOf<Op<P, M>, "query">;
  body?: BodyOf<Op<P, M>> | Blob | ArrayBuffer;
  /** Не пробовать обновить ключ при 401 (вход, обновление, выход). */
  noRefresh?: boolean;
}

/** Отказ ядра: код реестра (5.10, report.verdict) или error.code, иначе HTTP_<код>. */
export class PeopleError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public report?: Schemas["Refusal"]["report"],
    public fields: Record<string, unknown>[] = [],
  ) {
    super(message);
    this.name = "PeopleError";
  }
}

export interface PeopleOptions {
  /** Адрес ядра: "" — тот же сайт. */
  base?: () => string;
  /** Ключ входа человека; нет — запрос без него. */
  token?: () => string | null;
  /** Обновить ключ после 401 (одна попытка на всех — её держит хозяин сессии, лик); true — повторить запрос. */
  refresh?: () => Promise<boolean>;
  fetch?: typeof fetch;
}

export class People {
  constructor(private o: PeopleOptions = {}) {}

  /** Вызов адреса API людей по договору: people.call("get", "/api/v1/facts/{id}", { path: { id } }). */
  async call<P extends PeoplePath, M extends Method & keyof paths[P]>(
    method: M,
    path: P,
    opts: CallOptions<P, M> = {},
  ): Promise<Reply<P, M>> {
    const res = await this.raw(method, path, opts);
    if (res.status === 204) return undefined as Reply<P, M>;
    const data = (await res.json().catch(() => ({}))) as Reply<P, M>;
    return data;
  }

  /** Ответ как есть (файлы, выгрузка zip): ошибки — PeopleError, как у call. */
  async raw<P extends PeoplePath, M extends Method & keyof paths[P]>(
    method: M,
    path: P,
    opts: CallOptions<P, M> = {},
    retry = true,
  ): Promise<Response> {
    const url = (this.o.base?.() ?? "") + fill(String(path), opts.path as Record<string, unknown> | undefined) +
      search(opts.query as Record<string, unknown> | undefined);
    const headers: Record<string, string> = {};
    const token = this.o.token?.() ?? null;
    if (token) headers.Authorization = `Bearer ${token}`;
    let body: BodyInit | undefined;
    if (opts.body instanceof Blob) {
      headers["Content-Type"] = opts.body.type || "application/octet-stream"; // файл — сырым телом со своим типом
      body = opts.body;
    } else if (opts.body instanceof ArrayBuffer) {
      headers["Content-Type"] = "application/octet-stream";
      body = opts.body;
    }
    else if (opts.body !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(opts.body);
    }
    const doFetch = this.o.fetch ?? fetch;
    const res = await doFetch(url, { method: String(method).toUpperCase(), headers, body });
    if (res.status === 401 && retry && token && !opts.noRefresh && this.o.refresh && (await this.o.refresh())) {
      return this.raw(method, path, opts, false);
    }
    if (!res.ok) throw await refusal(res);
    return res;
  }
}

function fill(path: string, params?: Record<string, unknown>): string {
  return path.replace(/\{([^}]+)\}/g, (_, k: string) => {
    const v = params?.[k];
    if (v === undefined || v === null) throw new Error(`people: ${path} — нет части пути ${k}`);
    return encodeURIComponent(String(v));
  });
}

function search(q?: Record<string, unknown>): string {
  if (!q) return "";
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== null && v !== "") p.set(k, String(v));
  const s = p.toString();
  return s ? `?${s}` : "";
}

async function refusal(res: Response): Promise<PeopleError> {
  const data = (await res.json().catch(() => ({}))) as Partial<Schemas["Error"] & Schemas["Refusal"]>;
  const report = data.report;
  const code = report?.verdict ?? data.error?.code ?? `HTTP_${res.status}`;
  const message = report?.user_message ?? data.error?.message ?? res.statusText;
  return new PeopleError(res.status, String(code), String(message), report, (report?.field_errors ?? []) as Record<string, unknown>[]);
}
