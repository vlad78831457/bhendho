import type { Schemas } from "../offgrid";

// Типы контракта ядра (contracts/manifests/blank_envelope_schema.json, 5.7 спеки).
// Фронт не знает предметной области: только конверт, правила полей и общие секции отчёта.

export type FieldType =
  | "string" | "text" | "int" | "float" | "boolean" | "select" | "multiselect"
  | "date" | "file" | "fact_ref" | "group" | "list" | "mix";

export interface FieldRule {
  type: FieldType;
  required?: boolean;
  desc?: string | string[];
  default?: unknown;
  options?: string[];
  min?: number;
  max?: number;
  expected_fact_kind?: string;
  allowed_file_types?: string[];
  items?: Record<string, FieldRule>;
}

export type Rules = Record<string, FieldRule>;

export interface MetaUI {
  title: string;
  description?: string;
  category?: string;
  price_gc?: number;
  fields_rules: Rules;
  [k: string]: unknown;
}

export type TaskStatus = "draft" | "template" | "pending" | "processing" | "completed" | "failed" | "cancelled";

export interface FieldError {
  field: string;
  message: string;
}

export interface Report {
  verdict: string;
  user_message?: string;
  admin_message?: string;
  field_errors?: FieldError[];
  evidence?: string[];
  data_gaps?: string[];
  worker_charge?: { lines: { unit: string; units: number }[]; base_units?: number; total_gc?: number };
  result_fact?: { fact_id: string; version: number; status: string; kind?: string };
  [k: string]: unknown;
}

/** Задача — конверт ядра по договору; статус, правила полей и отчёт — точными типами панели (договор их держит открытыми). */
export type Envelope = Omit<Schemas["Task"], "system" | "meta_ui" | "report" | "commands"> & {
  system: Omit<Schemas["Task"]["system"], "status" | "kind"> & { status: TaskStatus; kind: "task" | "fact" };
  meta_ui: MetaUI;
  commands: Record<string, { status: string; label?: string; reason?: string }>;
  report: Report | null;
};

/** Бланк по договору; правила полей — точным типом панели. */
export type Template = Omit<Schemas["Template"], "system" | "meta_ui"> & {
  system: Omit<Schemas["Template"]["system"], "kind"> & { kind: "task" | "fact" };
  meta_ui: MetaUI;
};

/** Правила полей договор держит открытыми (их задаёт бланк, не ядро) — здесь они получают точный тип панели. */
function metaUI(m: Schemas["MetaUI"]): MetaUI {
  return { ...m, fields_rules: m.fields_rules as unknown as Rules };
}

/** Ответ ядра → конверт панели: каждое поле договора на месте, открытые части — точными типами. */
export function asEnvelope(t: Schemas["Task"]): Envelope {
  return {
    ...t,
    system: { ...t.system, status: t.system.status as TaskStatus, kind: t.system.kind as "task" | "fact" },
    meta_ui: metaUI(t.meta_ui),
    report: (t.report ?? null) as Report | null,
  };
}

export function asTemplate(t: Schemas["Template"]): Template {
  return { ...t, system: { ...t.system, kind: t.system.kind as "task" | "fact" }, meta_ui: metaUI(t.meta_ui) };
}

// --- Типы адресов API людей — из договора ядра (ADR-80, ADR-81): правку в ядре без правки здесь покажет tsc. ---
// Где договор нарочно открыт (правила полей, отчёт, значения бланка), панель держит свои точные типы выше.

export type CatalogItem = Schemas["CatalogItem"];
export type FactVersion = Schemas["FactVersion"];
/** Факт; в списке — ещё статус последней версии, дата и поля опубликованной версии (?fields=). */
export type Fact = Schemas["Fact"] & Partial<Pick<Schemas["FactListItem"], "status" | "created_at" | "summary">>;
export type Wallet = Schemas["Wallet"];
export type Transaction = Schemas["Transaction"];
export type Profile = Schemas["Profile"];
/** Внешний аккаунт, через который пользователь входит (Google, VK ID…). */
export type Identity = Schemas["Identity"];
/** Файл пользователя в ядре (ADR-60). */
export type StoredFile = Schemas["FileView"]["file"];
/** Подписанная ссылка на содержимое файла: без токена, до expires_at. */
export type FileLink = Schemas["Link"];
export type Tokens = Schemas["Tokens"];
/** Тарифы (ADR-69, ADR-78): что админ может поменять, и журнал изменений. */
export type Tariffs = Schemas["Tariffs"];
/** Факт подразделения: поля ядра и содержимое, которое ядро не понимает. */
export type ServiceFact = Schemas["ServiceFact"];
/** Бланк подразделения в обзоре админа (ADR-66); правила полей — точным типом панели. */
export type AdminForm = Omit<Schemas["AdminService"]["forms"][number], "fields"> & { fields?: Rules };
export type AdminService = Omit<Schemas["AdminService"], "forms"> & { forms: AdminForm[] };
export type Prorab = Schemas["Prorab"];
export type ShowcaseItem = Schemas["ShowcaseItem"];
export type DepartmentKey = Schemas["DepartmentKey"];

// Реестр realtime-событий (contracts/events/realtime_events.schema.json).
export interface RealtimeEvent {
  event_id: string;
  type: "task.progress" | "task.status" | "task.result" | "wallet.changed" | "fact.changed" | "system.snapshot";
  seq: number;
  occurred_at: string;
  payload: Record<string, unknown>;
}
