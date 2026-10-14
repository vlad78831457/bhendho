// Панель результата (спецификация 06, п.4): вердикт, объяснимость, доказательства, пробелы.
// Только общие секции конверта; уровень риска — не одним цветом, а ещё и словом и значком (a11y).

import type { Envelope, Report } from "../lib/types";
import { Markdown } from "./Markdown";

const VERDICT: Record<string, { label: string; tone: string }> = {
  SUCCESS: { label: "Готово", tone: "ok" },
  DATA_REJECTED: { label: "Данные не подходят", tone: "bad" },
  COMMAND_REFUSED: { label: "Команда отклонена", tone: "warn" },
  WORKER_TIMEOUT: { label: "Исполнитель не ответил", tone: "warn" },
};

const LEVEL: Record<string, { label: string; icon: string }> = {
  alert: { label: "Требует внимания", icon: "▲" },
  critical: { label: "Критично", icon: "▲" },
  attention: { label: "Наблюдать", icon: "●" },
  warning: { label: "Предупреждение", icon: "●" },
  info: { label: "К сведению", icon: "ⓘ" },
  error: { label: "Ошибка", icon: "▲" },
};

function Level({ level }: { level: string }) {
  const l = LEVEL[level] ?? { label: level, icon: "•" };
  return (
    <span className={`level level-${level}`}>
      <span aria-hidden="true">{l.icon}</span> {l.label}
    </span>
  );
}

type Card = {
  level: string; title: string; explanation?: string; how_computed?: string; confidence?: number;
  sample_size?: number; evidence?: { observation_id?: number; date?: string }[]; recommendations?: string[];
  metrics?: Record<string, unknown>; recommendation?: string; severity?: string; type?: string;
};

export function Verdict({ report }: { report: Report }) {
  const v = VERDICT[report.verdict] ?? { label: report.verdict, tone: "bad" };
  return (
    <p className={`verdict tone-${v.tone}`}>
      <strong>{v.label}</strong>
      {report.user_message && <> — {report.user_message}</>}
      <span className="code"> ({report.verdict})</span>
    </p>
  );
}

export function ReportView({ env }: { env: Envelope }) {
  const report = env.report;
  const result = env.result ?? {};
  if (!report) return null;
  const explanation = (result.explanation ?? report.explanation) as
    | { data_used?: string[]; period?: string; formulas?: string[]; assumptions?: string[]; as_of?: string; summary?: string }
    | undefined;
  const cards = (Array.isArray(result.cards) ? result.cards : []) as Card[];
  const alerts = (Array.isArray(result.items) && (result.items as Card[])[0]?.severity ? result.items : []) as Card[];
  const gaps = (Array.isArray(result.data_gaps) ? result.data_gaps : []) as { level: string; message: string }[];
  const warnings = (Array.isArray(result.warnings) ? result.warnings : []) as { level: string; message: string }[];
  const issues = (Array.isArray(report.issues) ? report.issues : []) as { index: number; message: string }[];

  return (
    <section className="report" aria-label="Результат">
      <Verdict report={report} />
      {report.field_errors?.length ? (
        <ul className="error">{report.field_errors.map((e) => <li key={e.field}>{e.field}: {e.message}</li>)}</ul>
      ) : null}
      {issues.length > 0 && (
        <ul className="error" aria-label="Ошибки в строках">
          {issues.map((i) => <li key={`${i.index}-${i.message}`}>Строка {i.index + 1}: {i.message}</li>)}
        </ul>
      )}
      {report.admin_message && (
        <details>
          <summary>Подробности для администратора</summary>
          <p>{report.admin_message}</p>
        </details>
      )}
      {warnings.length > 0 && (
        <ul className="cards">
          {warnings.map((w) => <li key={w.message} className={`card card-${w.level}`}><Level level={w.level} /> {w.message}</li>)}
        </ul>
      )}
      {explanation && (
        <div className="explanation">
          <h3>Как посчитано</h3>
          {explanation.summary && <p>{explanation.summary}</p>}
          {explanation.period && <p>Период: {explanation.period}</p>}
          {explanation.formulas && <><h4>Формулы</h4><ul>{explanation.formulas.map((f) => <li key={f}>{f}</li>)}</ul></>}
          {explanation.data_used && <><h4>Данные</h4><ul>{explanation.data_used.map((f) => <li key={f}>{f}</li>)}</ul></>}
          {explanation.assumptions && <><h4>Допущения</h4><ul>{explanation.assumptions.map((f) => <li key={f}>{f}</li>)}</ul></>}
          {explanation.as_of && <p className="hint">Данные на {explanation.as_of}</p>}
        </div>
      )}
      {[...cards, ...alerts].length > 0 && (
        <ul className="cards">
          {[...cards, ...alerts].map((c, i) => (
            <li key={`${c.title}-${i}`} className={`card card-${c.level ?? c.severity}`}>
              <Level level={c.level ?? c.severity ?? "info"} /> <strong>{c.title}</strong>
              {c.explanation && <p>{c.explanation}</p>}
              {c.recommendation && <p>{c.recommendation}</p>}
              {c.recommendations && <ul>{c.recommendations.map((r) => <li key={r}>{r}</li>)}</ul>}
              {(c.how_computed || c.metrics) && (
                <details>
                  <summary>
                    Основание{c.confidence !== undefined && ` · уверенность ${c.confidence}`}
                    {c.sample_size !== undefined && ` · выборка ${c.sample_size}`}
                  </summary>
                  {c.how_computed && <p>{c.how_computed}</p>}
                  {c.metrics && <pre className="mono">{JSON.stringify(c.metrics, null, 2)}</pre>}
                  {c.evidence?.length ? (
                    <p>Записи: {c.evidence.map((e) => `№${e.observation_id} (${e.date})`).join(", ")}</p>
                  ) : null}
                </details>
              )}
            </li>
          ))}
        </ul>
      )}
      {gaps.length > 0 && (
        <div className="gaps" role="note">
          <h3>Где данных недостаточно</h3>
          <ul>{gaps.map((g) => <li key={g.message}><Level level={g.level} /> {g.message}</li>)}</ul>
        </div>
      )}
      {report.result_fact && (
        <p>
          Результат сохранён: <a href={`#/fact/${report.result_fact.fact_id}`}>факт v{report.result_fact.version}</a> ({report.result_fact.status})
        </p>
      )}
      {report.worker_charge && (
        <p className="hint">
          Затраты исполнителя: {report.worker_charge.lines.map((l) => `${l.unit} ${l.units}`).join(", ")}
          {report.worker_charge.total_gc !== undefined && ` → ${report.worker_charge.total_gc} GC`}
        </p>
      )}
      {typeof result.text === "string" && result.format === "markdown" && <Markdown text={result.text} />}
    </section>
  );
}
