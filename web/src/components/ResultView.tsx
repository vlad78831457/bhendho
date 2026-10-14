// Универсальный просмотр результата задачи: фронт не знает формы результата —
// объект → «ключ: значение», массив объектов → таблица, массив чисел/строк → строка,
// null → «нет данных» (не ноль). Длинные наборы сворачиваются.

import type { ReactNode } from "react";

const MAX_ROWS = 50;
const HIDDEN = new Set(["explanation", "cards", "data_gaps", "warnings", "format", "text", "evidence"]);

function Scalar({ v }: { v: unknown }) {
  if (v === null || v === undefined) return <span className="muted">нет данных</span>;
  if (typeof v === "boolean") return <>{v ? "да" : "нет"}</>;
  return <>{String(v)}</>;
}

function isRecordArray(v: unknown): v is Record<string, unknown>[] {
  return Array.isArray(v) && v.length > 0 && v.every((x) => x && typeof x === "object" && !Array.isArray(x));
}

function Table({ rows }: { rows: Record<string, unknown>[] }) {
  const cols = [...new Set(rows.flatMap((r) => Object.keys(r)))].filter((c) => !HIDDEN.has(c));
  return (
    <div className="scroll">
      <table>
        <thead><tr>{cols.map((c) => <th key={c}>{c}</th>)}</tr></thead>
        <tbody>
          {rows.slice(0, MAX_ROWS).map((r, i) => (
            <tr key={i}>{cols.map((c) => <td key={c}><Value v={r[c]} depth={2} /></td>)}</tr>
          ))}
        </tbody>
      </table>
      {rows.length > MAX_ROWS && <p className="hint">Показаны первые {MAX_ROWS} из {rows.length}</p>}
    </div>
  );
}

export function Value({ v, depth = 0 }: { v: unknown; depth?: number }): ReactNode {
  if (Array.isArray(v)) {
    if (isRecordArray(v)) return depth > 2 ? <span>{v.length} записей</span> : <Table rows={v} />;
    if (v.every((x) => Array.isArray(x))) {
      return (
        <div className="scroll">
          <table className="matrix"><tbody>
            {(v as unknown[][]).slice(0, MAX_ROWS).map((row, i) => (
              <tr key={i}>{row.map((x, j) => <td key={j}><Scalar v={x} /></td>)}</tr>
            ))}
          </tbody></table>
        </div>
      );
    }
    return <>{v.map((x) => (x === null ? "—" : String(x))).join(", ")}</>;
  }
  if (v && typeof v === "object") {
    if (depth > 3) return <code>{JSON.stringify(v)}</code>;
    const entries = Object.entries(v as Record<string, unknown>).filter(([k]) => depth > 0 || !HIDDEN.has(k));
    return (
      <dl className="kv">
        {entries.map(([k, x]) => (
          <div key={k}>
            <dt>{k}</dt>
            <dd><Value v={x} depth={depth + 1} /></dd>
          </div>
        ))}
      </dl>
    );
  }
  if (typeof v === "string" && v.includes("\n")) return <pre className="mono">{v}</pre>;
  return <Scalar v={v} />;
}

export function ResultView({ result }: { result: Record<string, unknown> | undefined }) {
  if (!result || !Object.keys(result).length) return null;
  return (
    <section aria-label="Данные результата">
      <h3>Результат</h3>
      <Value v={result} />
    </section>
  );
}
