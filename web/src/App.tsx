import { useEffect, useState } from "react";

import { AuthPage, CatalogPage, FactPage, FactsPage, FormPage, TaskPage, TasksPage, WalletPage } from "./pages/Pages";
import { useSession } from "./session";

function useHash(): string {
  const [hash, setHash] = useState(location.hash || "#/catalog");
  useEffect(() => {
    const on = () => setHash(location.hash || "#/catalog");
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return hash;
}

const NET: Record<string, string> = { online: "в сети", connecting: "подключение…", offline: "нет связи" };

function Network() {
  const { api, realtime, net } = useSession();
  const [checking, setChecking] = useState(false);
  return (
    <div className="net" role="status" aria-live="polite">
      <span className={`dot dot-${net}`} aria-hidden="true" /> {NET[net]}
      {net !== "online" && (
        <button className="ghost small" disabled={checking} onClick={async () => {
          setChecking(true);
          await api.ping();
          realtime?.checkNow();
          setChecking(false);
        }}>
          Проверить сеть
        </button>
      )}
    </div>
  );
}

export function App() {
  const { api, user } = useSession();
  const hash = useHash();
  const [, route, arg] = hash.split("/");

  if (!api.authed) return <main className="container"><AuthPage /></main>;
  if (!user) return <main className="container"><p className="muted">Загрузка…</p></main>;
  // Фронт движка — только для админа (ADR-64); роль ставится на сервере (ADR-61).
  if (user.role !== "admin") {
    return (
      <main className="container">
        <h1>Нет доступа</h1>
        <p>Эта страница — для админа. Роль выдаётся на сервере командой <code>core user-role</code>.</p>
        <button className="ghost" onClick={() => api.logout()}>Выйти</button>
      </main>
    );
  }

  let page;
  switch (route) {
    case "form": page = <FormPage key={arg} formId={decodeURIComponent(arg ?? "")} />; break;
    case "tasks": page = <TasksPage />; break;
    case "task": page = <TaskPage key={arg} id={arg ?? ""} />; break;
    case "facts": page = <FactsPage />; break;
    case "fact": page = <FactPage key={arg} id={arg ?? ""} />; break;
    case "wallet": page = <WalletPage />; break;
    default: page = <CatalogPage />;
  }
  const link = (to: string, label: string) => (
    <a href={`#/${to}`} aria-current={route === to ? "page" : undefined}>{label}</a>
  );
  return (
    <>
      <header>
        <nav aria-label="Разделы">
          <strong>OFFGRID · админ</strong>
          {link("catalog", "Каталог")}
          {link("tasks", "Задачи")}
          {link("facts", "Данные")}
          {link("wallet", "Кошелёк")}
        </nav>
        <div className="me">
          <Network />
          <span className="muted">{user?.email}</span>
          <button className="ghost small" onClick={() => api.logout()}>Выйти</button>
        </div>
      </header>
      <main className="container">{page}</main>
    </>
  );
}
