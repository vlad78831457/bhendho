// Динамический рендерер бланка (DynamicBlankPanel из монолита, спецификация 06): поля строятся
// только по meta_ui.fields_rules — фронт не знает ни одной предметной формы.

import { useEffect, useId, useState } from "react";

import type { Api } from "../lib/api";
import { desc, errorsFor, initialValues, type Values } from "../lib/schema";
import type { FieldError, FieldRule, Rules } from "../lib/types";

interface FormProps {
  rules: Rules;
  values: Values;
  onChange: (v: Values) => void;
  errors: FieldError[];
  api: Api;
  prefix?: string;
}

export function DynamicForm({ rules, values, onChange, errors, api, prefix = "" }: FormProps) {
  return (
    <div className="fields">
      {Object.entries(rules).map(([name, rule]) => (
        <Field
          key={name}
          name={name}
          path={prefix ? `${prefix}.${name}` : name}
          rule={rule}
          value={values[name]}
          onChange={(v) => onChange({ ...values, [name]: v })}
          errors={errors}
          api={api}
        />
      ))}
    </div>
  );
}

interface FieldProps {
  name: string;
  path: string;
  rule: FieldRule;
  value: unknown;
  onChange: (v: unknown) => void;
  errors: FieldError[];
  api: Api;
}

function Field({ name, path, rule, value, onChange, errors, api }: FieldProps) {
  const id = useId();
  const own = errorsFor(errors, path);
  const hint = desc(rule);
  const described = [hint && `${id}-hint`, own.length && `${id}-err`].filter(Boolean).join(" ") || undefined;
  const label = (
    <label htmlFor={id}>
      {name}
      {rule.required && <span aria-label="обязательное"> *</span>}
    </label>
  );
  const common = { id, "aria-describedby": described, "aria-invalid": own.length > 0 || undefined };

  let control: React.ReactNode;
  switch (rule.type) {
    case "string":
    case "date":
      control = (
        <input
          {...common}
          type={rule.type === "date" ? "date" : "text"}
          value={(value as string) ?? ""}
          maxLength={rule.max}
          onChange={(e) => onChange(e.target.value || undefined)}
        />
      );
      break;
    case "text":
      control = (
        <textarea {...common} rows={4} value={(value as string) ?? ""} maxLength={rule.max}
          onChange={(e) => onChange(e.target.value || undefined)} />
      );
      break;
    case "int":
    case "float":
      control = (
        <input
          {...common}
          type="number"
          inputMode={rule.type === "int" ? "numeric" : "decimal"}
          step={rule.type === "int" ? 1 : "any"}
          min={rule.min}
          max={rule.max}
          value={value === undefined || value === null ? "" : String(value)}
          onChange={(e) => onChange(e.target.value === "" ? undefined : Number(e.target.value))}
        />
      );
      break;
    case "boolean":
      control = (
        <input {...common} type="checkbox" checked={value === true} onChange={(e) => onChange(e.target.checked)} />
      );
      break;
    case "select":
      control = (
        <select {...common} value={(value as string) ?? ""} onChange={(e) => onChange(e.target.value || undefined)}>
          <option value="">— не выбрано —</option>
          {rule.options?.map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </select>
      );
      break;
    case "multiselect": {
      const sel = new Set((value as string[]) ?? []);
      control = (
        <fieldset {...common} className="checks">
          {rule.options?.map((o) => (
            <label key={o} className="inline">
              <input
                type="checkbox"
                checked={sel.has(o)}
                onChange={(e) => onChange(rule.options!.filter((x) => (x === o ? e.target.checked : sel.has(x))))}
              />
              {o}
            </label>
          ))}
        </fieldset>
      );
      break;
    }
    case "fact_ref":
      control = <FactRef {...common} kind={rule.expected_fact_kind ?? ""} value={value as string} onChange={onChange} api={api} />;
      break;
    case "group":
      return (
        <fieldset className="group">
          <legend>{name}</legend>
          {hint && <p className="hint">{hint}</p>}
          <DynamicForm rules={rule.items ?? {}} values={(value as Values) ?? {}} onChange={onChange} errors={errors}
            api={api} prefix={path} />
        </fieldset>
      );
    case "list": {
      const rows = (value as Values[]) ?? [];
      const canAdd = rule.max === undefined || rows.length < rule.max;
      return (
        <fieldset className="group">
          <legend>
            {name} ({rows.length}){rule.required && " *"}
          </legend>
          {hint && <p className="hint">{hint}</p>}
          {rows.map((row, i) => (
            <div className="list-row" key={i}>
              <DynamicForm rules={rule.items ?? {}} values={row}
                onChange={(r) => onChange(rows.map((x, j) => (j === i ? r : x)))} errors={errors} api={api}
                prefix={`${path}[${i}]`} />
              <button type="button" className="ghost" onClick={() => onChange(rows.filter((_, j) => j !== i))}>
                Удалить строку {i + 1}
              </button>
            </div>
          ))}
          {canAdd && (
            <button type="button" onClick={() => onChange([...rows, initialValues(rule.items ?? {})])}>
              Добавить строку
            </button>
          )}
          <Errors id={`${id}-err`} messages={own} />
        </fieldset>
      );
    }
    case "file":
      control = <p className="hint">Загрузка файлов появится вместе с хранилищем ядра (цикл M3).</p>;
      break;
    case "mix":
      control = <MixInput {...common} value={value} onChange={onChange} />;
      break;
  }

  return (
    <div className={`field${own.length ? " has-error" : ""}`}>
      {label}
      {control}
      {hint && (
        <small id={`${id}-hint`} className="hint">
          {hint}
        </small>
      )}
      <Errors id={`${id}-err`} messages={own} />
    </div>
  );
}

function Errors({ id, messages }: { id: string; messages: string[] }) {
  if (!messages.length) return null;
  return (
    <p id={id} role="alert" className="error">
      {messages.join("; ")}
    </p>
  );
}

function MixInput({ value, onChange, ...rest }: { value: unknown; onChange: (v: unknown) => void; id: string }) {
  const [text, setText] = useState(value === undefined ? "" : JSON.stringify(value, null, 2));
  const [bad, setBad] = useState(false);
  return (
    <>
      <textarea {...rest} rows={4} className="mono" value={text}
        onChange={(e) => {
          setText(e.target.value);
          if (!e.target.value.trim()) {
            setBad(false);
            onChange(undefined);
            return;
          }
          try {
            onChange(JSON.parse(e.target.value));
            setBad(false);
          } catch {
            setBad(true);
          }
        }} />
      {bad && <p className="error">Неверный JSON</p>}
    </>
  );
}

/** Выбор своего опубликованного факта нужного kind (5.8: ставить задачу можно только на published). */
function FactRef({ kind, value, onChange, api, ...rest }: {
  kind: string; value: string | undefined; onChange: (v: unknown) => void; api: Api; id: string;
}) {
  const [options, setOptions] = useState<{ id: string; label: string }[] | null>(null);
  useEffect(() => {
    let alive = true;
    (async () => {
      const facts = (await api.facts(kind)).filter((f) => f.published_version !== null).slice(0, 50);
      const labelled = await Promise.all(
        facts.map(async (f) => {
          const full = await api.fact(f.fact_id).catch(() => null);
          const v = full?.versions.find((x) => x.version === f.published_version)?.values ?? {};
          const title = ["title", "name", "child_code"].map((k) => v[k]).find((x) => typeof x === "string");
          return { id: f.fact_id, label: `${title ?? f.fact_id.slice(0, 8)} · v${f.published_version}` };
        }),
      );
      if (alive) setOptions(labelled);
    })().catch(() => alive && setOptions([]));
    return () => {
      alive = false;
    };
  }, [api, kind]);
  if (options === null) return <p className="hint">Загрузка фактов…</p>;
  if (!options.length)
    return (
      <p className="hint">
        Нет опубликованных фактов вида «{kind}». <a href={`#/form/${kind}`}>Создать</a>
      </p>
    );
  return (
    <select {...rest} value={value ?? ""} onChange={(e) => onChange(e.target.value || undefined)}>
      <option value="">— выберите —</option>
      {options.map((o) => (
        <option key={o.id} value={o.id}>
          {o.label}
        </option>
      ))}
    </select>
  );
}
