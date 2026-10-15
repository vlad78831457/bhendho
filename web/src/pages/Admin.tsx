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

const day = (offset: number) => new Date(Date.now() + offset * 86400000).toISOString().slice(0, 10);

/** Выгрузка всей базы за период (ADR-67): архив по таблицам, без секретов. */
export function ExportBox() {
  const { api } = useSession();
  const [from, setFrom] = useState(day(-30));
  const [to, setTo] = useState(day(1));
  const [message, setMessage] = useState("");
  const run = async (e: FormEvent) => {
    e.preventDefault();
    setMessage("Готовлю архив…");
    try {
      const blob = await api.adminExport(from, to);
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = `offgrid-${from}-${to}.zip`;
      a.click();
      URL.revokeObjectURL(a.href);
      setMessage("Архив скачан.");
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    }
  };
  return (
    <form onSubmit={run} className="card">
      <h2>Выгрузка всей базы за период</h2>
      <label>С <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} /></label>{" "}
      <label>по (не включая) <input type="date" value={to} onChange={(e) => setTo(e.target.value)} /></label>{" "}
      <button type="submit">Выгрузить</button>
      {message && <p role="status">{message}</p>}
    </form>
  );
}

/** Подразделения и все их бланки — схема бланков для админа. */
export function DepartmentsPage() {
  const { api } = useSession();
  const { data, error } = useLoad<AdminService[]>(() => api.adminServices(), [api]);
  if (!data) return <Loading error={error} />;
  if (!data.length) return <p>Подразделений нет: ни один модуль не зарегистрирован.</p>;
  return (
    <>
      <h1>Подразделения</h1>
      <ExportBox />
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

/** Одна строка тарифа: значение из паспорта, действующее и поле для нового. */
function TariffRow({ label, manifest, value, override, onSave }: {
  label: string; manifest: string; value: string; override: boolean; onSave: (v: string | null) => Promise<void>;
}) {
  const [draft, setDraft] = useState("");
  return (
    <tr>
      <td>{label}</td>
      <td className="small">{manifest}</td>
      <td>{override ? <strong>{value}</strong> : value}</td>
      <td>
        <input aria-label={`новое: ${label}`} inputMode="decimal" value={draft} onChange={(e) => setDraft(e.target.value)} size={8} />{" "}
        <button type="button" disabled={!draft} onClick={() => void onSave(draft.replace(",", ".")).then(() => setDraft(""))}>Сохранить</button>{" "}
        {override && <button type="button" className="ghost" onClick={() => void onSave(null)}>Как в паспорте</button>}
      </td>
    </tr>
  );
}

/** Тарифы (ADR-69): меняются без перезапуска; задачи помнят тарифы на момент создания. */
export function TariffsPage() {
  const { api } = useSession();
  const { data, error, reload } = useLoad(() => api.adminTariffs(), [api]);
  const [message, setMessage] = useState("");
  if (!data) return <Loading error={error} />;
  const save = (scope: string, target: string) => async (v: string | null) => {
    try {
      await api.adminSetTariff(scope, target, v);
      setMessage("Сохранено. Действует для новых задач.");
      reload();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    }
  };
  return (
    <>
      <h1>Тарифы</h1>
      <p className="muted">Меняются сразу, без перезапуска. Задачи, созданные раньше, списываются по прежним тарифам.</p>
      {message && <p role="status">{message}</p>}
      <table>
        <thead>
          <tr><th>Что</th><th>В паспорте</th><th>Действует</th><th>Изменить</th></tr>
        </thead>
        <tbody>
          <tr><th colSpan={4}>Резерв задачи, GC</th></tr>
          {data.forms.map((f) => (
            <TariffRow key={f.form_id} label={`${f.title} (${f.service})`} manifest={f.manifest_price} value={f.price}
              override={f.override} onSave={save("form_price", f.form_id)} />
          ))}
          <tr><th colSpan={4}>Единицы подразделений</th></tr>
          {data.units.map((u) => (
            <TariffRow key={`${u.service}:${u.unit}`}
              label={`${u.service}: ${u.unit} — ${u.base ? "GC за единицу" : "основных единиц в одной"}`}
              manifest={u.manifest_rate} value={u.rate} override={u.override} onSave={save("unit_rate", `${u.service}:${u.unit}`)} />
          ))}
          <tr><th colSpan={4}>Минус</th></tr>
          <TariffRow label="Крышка списания, резервов" manifest={data.overdraft_factor.config} value={data.overdraft_factor.value}
            override={data.overdraft_factor.override} onSave={save("overdraft_factor", "")} />
        </tbody>
      </table>
      <h2>Изменения</h2>
      <table>
        <tbody>
          {data.history.map((h, i) => (
            <tr key={i}>
              <td className="small">{new Date(h.created_at).toLocaleString("ru-RU")}</td>
              <td className="small">{h.scope} {h.target}</td>
              <td>{h.value ?? "как в паспорте"}</td>
              <td className="small">{h.writer}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}
