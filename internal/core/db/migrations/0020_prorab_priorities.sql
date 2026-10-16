-- Уровни прорабов (план 7.3, ADR-76): админ задаёт приоритет прораба на бланк задачи. Нет строки — приоритет 1;
-- 0 — прораб этот бланк не берёт; задачу берёт прораб с наибольшим приоритетом среди действующих.
CREATE TABLE prorab_priorities (
    service_id  UUID NOT NULL REFERENCES services(id),
    prorab      TEXT NOT NULL,
    form_id     TEXT NOT NULL,
    priority    INT  NOT NULL CHECK (priority >= 0),
    updated_by  TEXT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (service_id, prorab, form_id)
);
