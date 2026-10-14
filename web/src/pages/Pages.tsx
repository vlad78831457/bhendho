// Экраны: вход, каталог, форма, задачи, задача, факты, факт, кошелёк. Ни один экран не знает
// предметной области — всё строится по каталогу, шаблонам и конвертам ядра.

import { useCallback, useEffect, useState, type FormEvent } from "react";

import { DynamicForm } from "../components/DynamicForm";
import { ReportView, Verdict } from "../components/ReportView";
import { ResultView, Value } from "../components/ResultView";
import { ApiError } from "../lib/api";
import { clean, initialValues, validate, type Values } from "../lib/schema";
import type { CatalogItem, Envelope, Fact, FieldError, Report, Template, Transaction, Wallet } from "../lib/types";
import { uuid } from "../lib/uuid";
import { useSession } from "../session";

const STATUS: Record<string, string> = {
  pending: "в очереди", processing: "выполняется", completed: "готово", failed: "не выполнено",
  cancelled: "отменено", draft: "черновик", template: "шаблон", published: "опубликован", archived: "в архиве",
  deleted: "удалён",
};

export function StatusBadge({ status }: { status: string }) {
  return <span className={`badge status-${status}`}>{STATUS[status] ?? status}</span>;
}

export function useLoad<T>(load: () => Promise<T>, deps: unknown[]): { data: T | null; error: string | null; reload: () => void } {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [n, setN] = useState(0);
  useEffect(() => {
    let alive = true;
    load()
      .then((d) => alive && (setData(d), setError(null)))
      .catch((e: unknown) => alive && setError(e instanceof Error ? e.message : String(e)));
    return () => {
      alive = false;
    };
  }, [...deps, n]);
  return { data, error, reload: () => setN((x) => x + 1) };
}

export function Loading({ error }: { error: string | null }) {
  return error ? <p role="alert" className="error">Ошибка: {error}</p> : <p aria-busy="true">Загрузка…</p>;
}

// --- вход -----------------------------------------------------------------------------------------

export function AuthPage() {
  const { api } = useSession();
  const [mode, setMode] = useState<"login" | "register">("login");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      if (mode === "login") await api.login(email, password);
      else await api.register(email, password);
      location.hash = "#/catalog";
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Нет связи с сервером");
    }
  };
  return (
    <form className="auth" onSubmit={submit} aria-label={mode === "login" ? "Вход" : "Регистрация"}>
      <h1>{mode === "login" ? "Вход" : "Регистрация"}</h1>
      <label htmlFor="email">Email</label>
      <input id="email" type="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} />
      <label htmlFor="password">Пароль</label>
      <input id="password" type="password" minLength={8} required autoComplete={mode === "login" ? "current-password" : "new-password"}
        value={password} onChange={(e) => setPassword(e.target.value)} />
      {error && <p role="alert" className="error">{error}</p>}
      <button type="submit">{mode === "login" ? "Войти" : "Зарегистрироваться"}</button>
      <button type="button" className="ghost" onClick={() => setMode(mode === "login" ? "register" : "login")}>
        {mode === "login" ? "Нет аккаунта? Регистрация" : "Уже есть аккаунт? Вход"}
      </button>
    </form>
  );
}

// --- каталог --------------------------------------------------------------------------------------

export function CatalogPage() {
  const { api } = useSession();
  const { data, error } = useLoad(() => api.catalog(), [api]);
  if (!data) return <Loading error={error} />;
  const groups = new Map<string, CatalogItem[]>();
  for (const f of data) {
    const cat = f.category ?? "Прочее";
    groups.set(cat, [...(groups.get(cat) ?? []), f]);
  }
  if (!data.length) return <p>Каталог пуст: ни один модуль ещё не зарегистрировал формы.</p>;
  return (
    <>
      <h1>Каталог</h1>
      {[...groups].map(([cat, items]) => (
        <section key={cat} aria-label={cat}>
          <h2>{cat}</h2>
          <ul className="catalog">
            {items.map((f) => (
              <li key={f.form_id}>
                <a href={`#/form/${f.form_id}`}>{f.title}</a>
                <span className="muted">
                  {f.kind === "fact" ? "данные" : "задача"}
                  {f.kind === "task" && ` · ${Number(f.price) === 0 ? "бесплатно" : `${Number(f.price)} ${f.currency_id}`}`}
                </span>
              </li>
            ))}
          </ul>
        </section>
      ))}
    </>
  );
}

// --- форма ----------------------------------------------------------------------------------------

interface Draft {
  values: Values;
  key: string; // idempotency_key: один на логическую отправку, не меняется при повторах (ADR-20)
}

export function FormPage({ formId }: { formId: string }) {
  const { api, kv } = useSession();
  const { data: tpl, error } = useLoad<Template>(() => api.template(formId), [api, formId]);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [errors, setErrors] = useState<FieldError[]>([]);
  const [refusal, setRefusal] = useState<Report | null>(null);
  const [busy, setBusy] = useState(false);
  const draftKey = `form:${formId}`;

  useEffect(() => {
    if (!tpl) return;
    let alive = true;
    (async () => {
      const saved = kv ? await kv.get<Draft>("drafts", draftKey) : undefined;
      if (alive) setDraft(saved ?? { values: initialValues(tpl.meta_ui.fields_rules), key: uuid() });
    })();
    return () => {
      alive = false;
    };
  }, [tpl, kv, draftKey]);

  const change = useCallback(
    (values: Values) => {
      setDraft((d) => {
        const next = { key: d?.key ?? uuid(), values };
        void kv?.put("drafts", draftKey, next); // ADR-42: начатое заполнение не теряется
        return next;
      });
    },
    [kv, draftKey],
  );

  if (!tpl || !draft) return <Loading error={error} />;
  const rules = tpl.meta_ui.fields_rules;
  const isTask = tpl.system.kind === "task";

  const submit = async (e: FormEvent, publish = true) => {
    e.preventDefault();
    const errs = validate(rules, draft.values);
    setErrors(errs);
    setRefusal(null);
    if (errs.length) return;
    setBusy(true);
    try {
      const values = clean(rules, draft.values);
      if (isTask) {
        const env = await api.createTask(formId, values, draft.key);
        await kv?.del("drafts", draftKey);
        location.hash = `#/task/${env.system.document_id}`;
      } else {
        let fact = await api.createFact(formId, values);
        if (publish) fact = await api.publishFact(fact.fact_id);
        await kv?.del("drafts", draftKey);
        location.hash = `#/fact/${fact.fact_id}`;
      }
    } catch (err) {
      if (err instanceof ApiError && err.report) {
        setRefusal(err.report);
        setErrors(err.report.field_errors ?? []);
      } else setRefusal({ verdict: "NETWORK", user_message: "Нет связи — черновик сохранён, отправьте позже" });
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={(e) => submit(e)} noValidate aria-label={tpl.meta_ui.title}>
      <h1>{tpl.meta_ui.title}</h1>
      {tpl.meta_ui.description && <p>{tpl.meta_ui.description}</p>}
      {isTask && (
        <p className="price">
          Резерв: {Number(tpl.system.price) === 0 ? "бесплатно" : `${Number(tpl.system.price)} GC`}
          <span className="hint"> — списывается по факту, не больше резерва</span>
        </p>
      )}
      <DynamicForm rules={rules} values={draft.values} onChange={change} errors={errors} api={api} />
      {errors.length > 0 && !refusal && <p role="alert" className="error">Проверьте поля: {errors.length}</p>}
      {refusal && <Verdict report={refusal} />}
      <div className="actions">
        <button type="submit" disabled={busy}>{isTask ? "Запустить" : "Сохранить и опубликовать"}</button>
        {!isTask && (
          <button type="button" className="ghost" disabled={busy} onClick={(e) => submit(e as unknown as FormEvent, false)}>
            Сохранить черновик
          </button>
        )}
      </div>
    </form>
  );
}

// --- задачи ---------------------------------------------------------------------------------------

export function TasksPage() {
  const { api, tick, progress } = useSession();
  const [status, setStatus] = useState("");
  const [offset, setOffset] = useState(0);
  const { data, error } = useLoad(() => api.tasks(status, 20, offset), [api, status, offset, tick]);
  return (
    <>
      <h1>Задачи</h1>
      <label htmlFor="status">Статус</label>
      <select id="status" value={status} onChange={(e) => (setStatus(e.target.value), setOffset(0))}>
        <option value="">все</option>
        {["pending", "processing", "completed", "failed", "cancelled"].map((s) => (
          <option key={s} value={s}>{STATUS[s]}</option>
        ))}
      </select>
      {!data ? <Loading error={error} /> : data.items.length === 0 ? <p>Задач нет.</p> : (
        <table>
          <thead><tr><th>Форма</th><th>Статус</th><th>Создана</th></tr></thead>
          <tbody>
            {data.items.map((t) => {
              const p = progress[t.system.document_id];
              return (
                <tr key={t.system.document_id}>
                  <td><a href={`#/task/${t.system.document_id}`}>{t.meta_ui.title}</a></td>
                  <td>
                    <StatusBadge status={t.system.status} />
                    {t.system.status === "processing" && p && <progress max={100} value={p.percent} aria-label={p.phase} />}
                  </td>
                  <td>{new Date(t.system.created_at).toLocaleString("ru-RU")}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
      {data && data.total > 20 && (
        <nav className="actions" aria-label="Страницы">
          <button disabled={offset === 0} onClick={() => setOffset(offset - 20)}>Назад</button>
          <span>{offset + 1}–{Math.min(offset + 20, data.total)} из {data.total}</span>
          <button disabled={offset + 20 >= data.total} onClick={() => setOffset(offset + 20)}>Дальше</button>
        </nav>
      )}
    </>
  );
}

export function TaskPage({ id }: { id: string }) {
  const { api, tick, progress } = useSession();
  const { data: env, error, reload } = useLoad<Envelope>(() => api.task(id), [api, id, tick]);
  const [cmdError, setCmdError] = useState<Report | null>(null);
  if (!env) return <Loading error={error} />;
  const p = progress[id];
  const run = async (cmd: string) => {
    setCmdError(null);
    try {
      const next = await api.command(id, cmd, cmd === "retry" ? uuid() : undefined);
      if (next.system.document_id !== id) location.hash = `#/task/${next.system.document_id}`;
      else reload();
    } catch (err) {
      if (err instanceof ApiError && err.report) setCmdError(err.report);
    }
  };
  const labels: Record<string, string> = { cancel: "Отменить", retry: "Повторить" };
  return (
    <article>
      <h1>{env.meta_ui.title}</h1>
      <p>
        <StatusBadge status={env.system.status} />
        {env.system.attempts ? <span className="muted"> · попыток: {env.system.attempts}</span> : null}
      </p>
      {env.system.status === "processing" && (
        <div aria-live="polite">
          <progress max={100} value={p?.percent ?? 0} /> {p ? `${p.percent}% · ${p.phase}` : "выполняется…"}
        </div>
      )}
      {env.system.status === "pending" && <p aria-live="polite">В очереди — ждёт исполнителя.</p>}
      <div className="actions">
        {Object.entries(env.commands).map(([name, c]) => (
          <button key={name} disabled={c.status !== "todo"} title={c.reason} onClick={() => run(name)}>
            {c.label ?? labels[name] ?? name}
          </button>
        ))}
      </div>
      {cmdError && <Verdict report={cmdError} />}
      <ReportView env={env} />
      <ResultView result={env.result} />
      <details>
        <summary>Входные данные</summary>
        <Value v={env.values} />
      </details>
    </article>
  );
}

// --- факты ----------------------------------------------------------------------------------------

export function FactsPage() {
  const { api, tick } = useSession();
  const { data, error } = useLoad(() => api.facts(), [api, tick]);
  if (!data) return <Loading error={error} />;
  return (
    <>
      <h1>Мои данные</h1>
      {data.length === 0 ? <p>Фактов пока нет — создайте их из каталога («данные»).</p> : (
        <table>
          <thead><tr><th>Вид</th><th>Опубликованная версия</th><th>Происхождение</th></tr></thead>
          <tbody>
            {data.map((f) => (
              <tr key={f.fact_id}>
                <td><a href={`#/fact/${f.fact_id}`}>{f.kind}</a></td>
                <td>{f.published_version ?? <span className="muted">не опубликован</span>}</td>
                <td>{f.origin}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}

export function FactPage({ id }: { id: string }) {
  const { api, tick } = useSession();
  const { data: fact, error, reload } = useLoad<Fact>(() => api.fact(id), [api, id, tick]);
  if (!fact) return <Loading error={error} />;
  return (
    <article>
      <h1>{fact.kind}</h1>
      <p className="muted">Происхождение: {fact.origin} · id {fact.fact_id}</p>
      {fact.versions.map((v) => (
        <details key={v.version} open={v.version === (fact.published_version ?? fact.versions[0]?.version)}>
          <summary>
            Версия {v.version} <StatusBadge status={v.status} />
          </summary>
          {(v.status === "draft" || v.status === "archived") && (
            <button onClick={async () => (await api.publishFact(fact.fact_id, v.version), reload())}>
              Опубликовать версию {v.version}
            </button>
          )}
          <Value v={v.values} />
        </details>
      ))}
    </article>
  );
}

// --- кошелёк --------------------------------------------------------------------------------------

export function WalletPage() {
  const { api, tick } = useSession();
  const { data, error, reload } = useLoad<[Wallet[], Transaction[]]>(
    () => Promise.all([api.wallets(), api.transactions()]), [api, tick],
  );
  const [amount, setAmount] = useState(100);
  const [msg, setMsg] = useState<string | null>(null);
  const [code, setCode] = useState("");
  const [promoMsg, setPromoMsg] = useState<string | null>(null);
  if (!data) return <Loading error={error} />;
  const [wallets, txs] = data;
  return (
    <>
      <h1>Кошелёк</h1>
      {wallets.map((w) => (
        <p key={w.wallet_id} className="balance">
          Доступно <strong>{Number(w.available)} {w.currency_id}</strong>
          <span className="muted"> · баланс {Number(w.balance)}, в резерве {Number(w.reserved)}</span>
        </p>
      ))}
      <form className="actions" onSubmit={async (e) => {
        e.preventDefault();
        setMsg(null);
        try {
          await api.demoDeposit(amount);
          reload();
        } catch (err) {
          setMsg(err instanceof ApiError ? err.message : "Нет связи");
        }
      }}>
        <label htmlFor="amount">Демо-пополнение</label>
        <input id="amount" type="number" min={1} value={amount} onChange={(e) => setAmount(Number(e.target.value))} />
        <button type="submit">Пополнить</button>
      </form>
      {msg && <p role="alert" className="error">{msg}</p>}
      <form className="actions" onSubmit={async (e) => {
        e.preventDefault();
        setPromoMsg(null);
        try {
          const res = await api.redeemPromo(code);
          setPromoMsg(res.already_redeemed ? "Этот промокод уже зачислен вам" : `Зачислено ${Number(res.amount)} ${res.currency_id}`);
          setCode("");
          reload();
        } catch (err) {
          setPromoMsg(err instanceof ApiError ? `${err.code}: ${err.message}` : "Нет связи");
        }
      }}>
        <label htmlFor="promo">Промокод</label>
        <input id="promo" value={code} onChange={(e) => setCode(e.target.value)} placeholder="XXXX-XXXX-XXXX-XXXX" />
        <button type="submit">Активировать</button>
      </form>
      {promoMsg && <p role="status">{promoMsg}</p>}
      <h2>Операции</h2>
      <table>
        <thead><tr><th>Когда</th><th>Операция</th><th>Сумма</th><th>Задача</th></tr></thead>
        <tbody>
          {txs.map((t) => (
            <tr key={t.id}>
              <td>{new Date(t.created_at).toLocaleString("ru-RU")}</td>
              <td>{t.type}</td>
              <td>{Number(t.amount)}</td>
              <td>{t.task_id ? <a href={`#/task/${t.task_id}`}>открыть</a> : "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}
