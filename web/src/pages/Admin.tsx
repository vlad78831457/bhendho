// Простая панель админа (ADR-66): подразделения и их бланки, память подразделения, настройки.
// Как и остальной фронт, ничего не знает о предметной области: формы и поля — из ядра.

import { useState, type FormEvent } from "react";

import { DynamicForm } from "../components/DynamicForm";
import { Value } from "../components/ResultView";
import { initialValues, type Values } from "../lib/schema";
import type { AdminService, Rules } from "../lib/types";
import { useSession } from "../session";
import { Loading, useLoad } from "./Pages";

const factLink = (service: string, formId: string) =>
  `#/dept/${encodeURIComponent(service)}/${encodeURIComponent(formId)}`;

/** Подразделения и все их бланки — схема бланков для админа. */
export function DepartmentsPage() {
  const { api } = useSession();
  const { data, error } = useLoad<AdminService[]>(() => api.adminServices(), [api]);
  if (!data) return <Loading error={error} />;
  if (!data.length) return <p>Подразделений нет: ни один модуль не зарегистрирован.</p>;
  return (
    <>
      <h1>Подразделения</h1>
      {data.map((s) => (
        <section key={s.code} className="card">
          <h2>{s.title}</h2>
          <p className="muted">
            {s.code} · {s.module_id} {s.module_version}
          </p>
          <table>
            <thead>
              <tr><th>Бланк</th><th>Вид</th><th>Кому</th><th>Цена</th><th>Поля</th></tr>
            </thead>
            <tbody>
              {s.forms.map((f) => (
                <tr key={f.form_id}>
                  <td>
                    {f.kind === "fact" && f.audience !== "user" ? <a href={factLink(s.code, f.form_id)}>{f.title}</a> : f.title}
                    <div className="muted small">{f.form_id} v{f.version}</div>
                  </td>
                  <td>{f.kind === "fact" ? "факт" : "задача"}</td>
                  <td>{f.audience}</td>
                  <td>{f.kind === "task" ? f.price : ""}</td>
                  <td className="small">{Object.keys(f.fields ?? {}).join(", ")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      ))}
    </>
  );
}

/** Память подразделения по одному бланку: последние 100 и запись факта от админа. */
export function DepartmentFactsPage({ service, formId }: { service: string; formId: string }) {
  const { api } = useSession();
  const { data, error, reload } = useLoad(
    () => Promise.all([api.adminServices(), api.adminFacts(service, formId)]),
    [api, service, formId],
  );
  const [values, setValues] = useState<Values | null>(null);
  const [message, setMessage] = useState("");
  if (!data) return <Loading error={error} />;
  const [services, facts] = data;
  const form = services.find((s) => s.code === service)?.forms.find((f) => f.form_id === formId);
  const rules: Rules = form?.fields ?? {};
  const current = values ?? initialValues(rules);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    try {
      await api.adminWriteFact(service, formId, current);
      setValues(initialValues(rules));
      setMessage("Записано.");
      reload();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <>
      <p><a href="#/depts">← Подразделения</a></p>
      <h1>{form?.title ?? formId}</h1>
      <p className="muted">{service} · {formId} · кому: {form?.audience ?? "?"}</p>

      <form onSubmit={submit} className="card">
        <h2>Записать факт</h2>
        <DynamicForm rules={rules} values={current} onChange={setValues} errors={[]} api={api} />
        <button type="submit">Записать</button>
        {message && <p role="status">{message}</p>}
      </form>

      <h2>Последние {facts.length}</h2>
      {facts.length === 0 ? (
        <p>Фактов пока нет.</p>
      ) : (
        <table>
          <thead>
            <tr><th>Время</th><th>Кто прислал</th><th>Задача</th><th>Содержимое</th></tr>
          </thead>
          <tbody>
            {facts.map((f) => (
              <tr key={f.id}>
                <td className="small">{new Date(f.created_at).toLocaleString("ru-RU")}</td>
                <td className="small">{f.writer}</td>
                <td className="small">{f.task_id ?? "—"}</td>
                <td><Value v={f.values} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}

/** Настройки ядра «как есть» — только чтение, без секретов. */
export function SettingsPage() {
  const { api } = useSession();
  const { data, error } = useLoad(() => api.adminSettings(), [api]);
  if (!data) return <Loading error={error} />;
  return (
    <>
      <h1>Настройки ядра</h1>
      <p className="muted">Только просмотр. Меняются на сервере.</p>
      <table>
        <tbody>
          {Object.entries(data)
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([k, v]) => (
              <tr key={k}><th>{k}</th><td>{String(v)}</td></tr>
            ))}
        </tbody>
      </table>
    </>
  );
}
