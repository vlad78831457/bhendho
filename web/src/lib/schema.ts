// Разбор fields_rules на клиенте. Проверки — зеркало internal/core/forms (те же пути полей
// «a.b», «list[0].x»), только чтобы подсветить ошибки до отправки; решение всегда за сервером
// (FR-IF-2). Пустые необязательные поля не отправляются: ядро материализует только написанное.

import type { FieldError, FieldRule, Rules } from "./types";

export type Values = Record<string, unknown>;

export function desc(rule: FieldRule): string {
  return Array.isArray(rule.desc) ? rule.desc.join(" ") : (rule.desc ?? "");
}

export function initialValues(rules: Rules): Values {
  const out: Values = {};
  for (const [name, r] of Object.entries(rules)) {
    if (r.default !== undefined) out[name] = r.default;
    else if (r.type === "list") out[name] = [];
    else if (r.type === "multiselect") out[name] = [];
    else if (r.type === "group" && r.items) out[name] = initialValues(r.items);
  }
  return out;
}

function empty(v: unknown): boolean {
  return v === undefined || v === null || v === "";
}

const DATE = /^\d{4}-\d{2}-\d{2}$/;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function validate(rules: Rules, values: Values, prefix = ""): FieldError[] {
  const errs: FieldError[] = [];
  const path = (n: string) => (prefix ? `${prefix}.${n}` : n);
  for (const [name, r] of Object.entries(rules)) {
    const v = values[name];
    const p = path(name);
    if (empty(v)) {
      if (r.required) errs.push({ field: p, message: "обязательное поле" });
      continue;
    }
    const bounds = (x: number, what: string) => {
      if (r.min !== undefined && x < r.min) errs.push({ field: p, message: `${what} не меньше ${r.min}` });
      if (r.max !== undefined && x > r.max) errs.push({ field: p, message: `${what} не больше ${r.max}` });
    };
    switch (r.type) {
      case "string":
      case "text":
        if (typeof v !== "string") errs.push({ field: p, message: "должна быть строка" });
        else bounds([...v].length, "длина");
        break;
      case "int":
        if (typeof v !== "number" || !Number.isInteger(v)) errs.push({ field: p, message: "целое число" });
        else bounds(v, "значение");
        break;
      case "float":
        if (typeof v !== "number" || Number.isNaN(v)) errs.push({ field: p, message: "число" });
        else bounds(v, "значение");
        break;
      case "boolean":
        if (typeof v !== "boolean") errs.push({ field: p, message: "да или нет" });
        break;
      case "date":
        if (typeof v !== "string" || !DATE.test(v)) errs.push({ field: p, message: "дата ГГГГ-ММ-ДД" });
        break;
      case "select":
        if (!r.options?.includes(v as string)) errs.push({ field: p, message: "выберите вариант из списка" });
        break;
      case "multiselect": {
        const arr = v as unknown[];
        if (!Array.isArray(arr) || arr.some((x) => !r.options?.includes(x as string)))
          errs.push({ field: p, message: "варианты из списка" });
        else bounds(arr.length, "выбрано");
        break;
      }
      case "fact_ref":
      case "file":
        if (typeof v !== "string" || !UUID.test(v)) errs.push({ field: p, message: "выберите значение" });
        break;
      case "group":
        errs.push(...validate(r.items ?? {}, (v as Values) ?? {}, p));
        break;
      case "list": {
        const rows = v as Values[];
        bounds(rows.length, "строк");
        rows.forEach((row, i) => errs.push(...validate(r.items ?? {}, row, `${p}[${i}]`)));
        break;
      }
      case "mix":
        break;
    }
  }
  return errs;
}

/** Значения к отправке: без пустых необязательных полей; группы и строки списков — рекурсивно. */
export function clean(rules: Rules, values: Values): Values {
  const out: Values = {};
  for (const [name, r] of Object.entries(rules)) {
    const v = values[name];
    if (empty(v)) continue;
    if (r.type === "group") {
      const g = clean(r.items ?? {}, v as Values);
      if (Object.keys(g).length) out[name] = g;
    } else if (r.type === "list") {
      const rows = (v as Values[]).map((row) => clean(r.items ?? {}, row));
      if (rows.length || r.required) out[name] = rows;
    } else if (r.type === "multiselect") {
      if ((v as unknown[]).length) out[name] = v;
    } else {
      out[name] = v;
    }
  }
  return out;
}

/** Ошибки к полю (включая вложенные: «notes[0].pic» относится к «notes»). */
export function errorsFor(errors: FieldError[], path: string): string[] {
  return errors.filter((e) => e.field === path).map((e) => e.message);
}
