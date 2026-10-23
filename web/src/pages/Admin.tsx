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

/** Ключ для нового или обновляемого подразделения (ADR-70): подразделение подключается с ним само. */
export function DepartmentKeyBox() {
  const { api } = useSession();
  const { data: keys, reload } = useLoad(() => api.adminDepartmentKeys(), [api]);
  const [code, setCode] = useState("");
  const [name, setName] = useState("main");
  const [result, setResult] = useState("");
  const issue = async (e: FormEvent) => {
    e.preventDefault();
    try {
      const r = await api.adminDepartmentKey(code.trim(), name.trim());
      setResult(`Ключ для ${r.code} / ${r.name}: ${r.key} — сохраните сейчас, больше он не покажется.`);
      reload();
    } catch (err) {
      setResult(err instanceof Error ? err.message : String(err));
    }
  };
  return (
    <>
    <form onSubmit={issue} className="card">
      <h2>Ключ подразделения</h2>
      <label>Код <input value={code} onChange={(e) => setCode(e.target.value)} placeholder="taleweaver" /></label>{" "}
      <label>Прораб <input value={name} onChange={(e) => setName(e.target.value)} /></label>{" "}
      <button type="submit" disabled={!code.trim()}>Выдать ключ</button>
      {result && <p role="status" className="small">{result}</p>}
    </form>
    {keys && keys.length > 0 && (
      <ul className="small">
        {keys.map((k) => (
          <li key={`${k.code}/${k.name}`}>
            {k.code} / {k.name}: {k.connected ? `подключён` : "выдан, прораб ещё не подключался"}
          </li>
        ))}
      </ul>
    )}
    </>
  );
}

/** Подразделения и все их бланки — схема бланков для админа. */
export function DepartmentsPage() {
  const { api } = useSession();
  const { data, error, reload } = useLoad<AdminService[]>(() => api.adminServices(), [api]);
  if (!data) return <Loading error={error} />;
  const toggle = (s: AdminService) => void api.adminSetEnabled(s.code, !s.enabled).then(reload);
  return (
    <>
      <h1>Подразделения</h1>
      <ExportBox />
      <DepartmentKeyBox />
      {!data.length && <p>Подразделений нет: ни один модуль не зарегистрирован.</p>}
      {data.map((s) => (
        <section key={s.code} className="card">
          <h2>{s.title}{!s.enabled && " — выключено"}</h2>
          <p className="muted">
            {s.code} · {s.module_id} {s.module_version}{" "}
            <button type="button" className="ghost small" onClick={() => toggle(s)}>{s.enabled ? "Выключить" : "Включить"}</button>
          </p>
          <ProrabsBox service={s.code} />
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
          <tr><th colSpan={4}>Коэффициенты цены: ядро × подразделение × прораб (резерв — × ядро и подразделение)</th></tr>
          {data.multipliers.map((m) => (
            <TariffRow key={m.target} label={m.target === "core" ? m.title : `${m.target}${m.title ? ` — ${m.title}` : ""}`}
              manifest="1" value={m.value} override={m.override} onSave={save("multiplier", m.target)} />
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

/** Витрина (ADR-74): свои опубликованные факты админа на посадочную страницу, по языкам и порядку. */
export function ShowcasePage() {
  const { api } = useSession();
  const { data, error, reload } = useLoad(() => Promise.all([api.adminShowcase(), api.facts()]), [api]);
  const [factId, setFactId] = useState("");
  const [language, setLanguage] = useState("ru");
  const [position, setPosition] = useState("0");
  const [message, setMessage] = useState("");
  if (!data) return <Loading error={error} />;
  const [items, mine] = data;
  const add = async (e: FormEvent) => {
    e.preventDefault();
    try {
      await api.adminAddShowcase(factId, language.trim(), Number(position) || 0);
      setMessage("На витрине.");
      reload();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    }
  };
  const label = (v: Record<string, unknown>) => String(v.title ?? v.name ?? "") || "без названия";
  return (
    <>
      <h1>Витрина</h1>
      <p className="muted">Ваши опубликованные факты для посадочной страницы. Посетитель видит витрину своего языка.</p>
      <form onSubmit={add} className="card">
        <label>Факт{" "}
          <select value={factId} onChange={(e) => setFactId(e.target.value)}>
            <option value="">—</option>
            {mine.filter((f) => f.published_version).map((f) => (
              <option key={f.fact_id} value={f.fact_id}>{f.kind} · {f.fact_id.slice(0, 8)}</option>
            ))}
          </select>
        </label>{" "}
        <label>Язык <input value={language} onChange={(e) => setLanguage(e.target.value)} size={3} /></label>{" "}
        <label>Порядок <input value={position} onChange={(e) => setPosition(e.target.value)} size={3} inputMode="numeric" /></label>{" "}
        <button type="submit" disabled={!factId}>На витрину</button>
        {message && <p role="status">{message}</p>}
      </form>
      <table>
        <thead><tr><th>Язык</th><th>Порядок</th><th>Что</th><th /></tr></thead>
        <tbody>
          {items.map((it) => (
            <tr key={it.id}>
              <td>{it.language}</td><td>{it.position}</td><td>{label(it.values)} <span className="muted small">{it.kind}</span></td>
              <td><button type="button" className="ghost small" onClick={() => void api.adminRemoveShowcase(it.id).then(reload)}>Убрать</button></td>
            </tr>
          ))}
        </tbody>
      </table>
      <h2>Публичные факты</h2>
      <p className="muted">На публичный факт любой человек ссылается в своих задачах без копии — пресеты, миры, случаи студии (ADR-79).</p>
      <table>
        <thead><tr><th>Факт</th><th>Видимость</th><th /></tr></thead>
        <tbody>
          {mine.filter((f) => f.published_version).map((f) => {
            const pub = f.visibility === "public";
            return (
              <tr key={f.fact_id}>
                <td>{f.kind} <span className="muted small">{f.fact_id.slice(0, 8)}</span></td>
                <td>{pub ? "публичный" : "приватный"}</td>
                <td><button type="button" className="ghost small"
                  onClick={() => void api.adminSetVisibility(f.fact_id, pub ? "private" : "public").then(reload)}>
                  {pub ? "Сделать приватным" : "Сделать публичным"}</button></td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </>
  );
}

/** Уровни прорабов подразделения (ADR-76): кто берёт какой бланк. Больше число — выше; 0 — не берёт. */
export function ProrabsBox({ service }: { service: string }) {
  const { api } = useSession();
  const { data, reload } = useLoad(() => api.adminProrabs(service), [api, service]);
  if (!data || !data.length) return null;
  const forms = [...new Set(data.map((p) => p.form_id))];
  const prorabs = [...new Set(data.map((p) => p.prorab))];
  const at = (p: string, f: string) => data.find((x) => x.prorab === p && x.form_id === f);
  const seen = (t?: string | null) => (t ? `на связи: ${new Date(t).toLocaleString("ru-RU")}` : "ещё не выходил на связь");
  return (
    <table className="small">
      <thead><tr><th>Прораб</th>{forms.map((f) => <th key={f}>{f}</th>)}</tr></thead>
      <tbody>
        {prorabs.map((p) => (
          <tr key={p}>
            <td>
              <strong>{p}</strong> {at(p, forms[0])?.title}
              <div className="muted small">{at(p, forms[0])?.description}</div>
              <div className="muted small">{seen(at(p, forms[0])?.last_seen_at)}</div>
            </td>
            {forms.map((f) => (
              <td key={f}>
                <input aria-label={`приоритет ${p} ${f}`} type="number" min={0} max={1000} size={4}
                  defaultValue={at(p, f)?.priority ?? 1}
                  onBlur={(e) => void api.adminSetProrab(service, p, f, Number(e.target.value) || 0).then(reload)} />
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}
