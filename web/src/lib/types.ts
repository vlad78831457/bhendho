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

export interface Envelope {
  system: {
    document_id: string;
    form_id: string;
    schema_version: number;
    kind: "task" | "fact";
    status: TaskStatus;
    created_at: string;
    updated_at?: string;
    attempts?: number;
    target_service?: string;
  };
  meta_ui: MetaUI;
  commands: Record<string, { status: string; label?: string; reason?: string }>;
  values: Record<string, unknown>;
  report: Report | null;
  result?: Record<string, unknown>;
}

export interface CatalogItem {
  form_id: string;
  schema_version: number;
  kind: "task" | "fact";
  title: string;
  service?: string;
  price: string;
  currency_id: string;
  category?: string;
}

export interface Template {
  system: { form_id: string; schema_version: number; kind: "task" | "fact"; target_service: string; price: string };
  meta_ui: MetaUI;
}

export interface FactVersion {
  version: number;
  status: string;
  schema_version: number;
  values: Record<string, unknown>;
  created_at: string;
}

export interface Fact {
  fact_id: string;
  kind: string;
  origin: string;
  published_version: number | null;
  versions: FactVersion[];
  /** Только в списке: статус последней версии, создан, поля опубликованной версии (?fields=). */
  status?: string;
  created_at?: string;
  summary?: Record<string, unknown>;
}

export interface Wallet {
  wallet_id: string;
  currency_id: string;
  balance: string;
  reserved: string;
  available: string;
}

export interface Transaction {
  id: number;
  type: string;
  amount: string;
  task_id?: string;
  form_id?: string;
  /** Откуда пополнение: demo, promo:<эмитент>. */
  source?: string;
  created_at: string;
}

export interface Profile {
  user_id: string;
  email: string;
  display_name: string;
  has_password: boolean;
  /** user | tester | admin — ставится только на сервере (ADR-61). */
  role?: string;
  created_at: string;
}

/** Внешний аккаунт, через который пользователь входит (Google, VK ID…). */
export interface Identity {
  provider: string;
  subject: string;
  email?: string;
  display_name: string;
  created_at: string;
  last_login_at?: string;
}

/** Файл пользователя в ядре (ADR-60). */
export interface StoredFile {
  file_id: string;
  filename: string;
  mime: string;
  size: number;
  width?: number;
  height?: number;
  created_at: string;
}

/** Подписанная ссылка на содержимое файла: без токена, до expires_at. */
export interface FileLink {
  url: string;
  expires_at: string;
}

export interface Tokens {
  access_token: string;
  refresh_token: string;
  expires_in: number;
}

// Реестр realtime-событий (contracts/events/realtime_events.schema.json).
export interface RealtimeEvent {
  event_id: string;
  type: "task.progress" | "task.status" | "task.result" | "wallet.changed" | "fact.changed" | "system.snapshot";
  seq: number;
  occurred_at: string;
  payload: Record<string, unknown>;
}

/** Бланк подразделения в обзоре админа (ADR-66). */
export interface AdminForm {
  form_id: string;
  version: number;
  kind: "fact" | "task";
  /** «Кому» (ADR-65): user — пользователю, иначе — память подразделения. */
  audience: string;
  title: string;
  price: string;
  fields?: Rules;
}

export interface AdminService {
  code: string;
  title: string;
  module_id: string;
  module_version: string;
  forms: AdminForm[];
}

/** Факт подразделения: семь полей ядра и содержимое, которое ядро не понимает. */
export interface ServiceFact {
  id: number;
  service: string;
  form_id: string;
  schema_version: number;
  audience: string;
  task_id: string | null;
  user_id: string | null;
  /** prorab:<имя> или admin:<id> */
  writer: string;
  created_at: string;
  values: Record<string, unknown>;
}
