package engine

// Разметка общей территории фронта (ADR-72): ядро отдаёт её как есть, по включённым подразделениям.

import (
	"context"
	"encoding/json"
)

// ServiceViews — разметка одного подразделения.
type ServiceViews struct {
	Service string           `json:"service"`
	Title   string           `json:"title"`
	Views   []map[string]any `json:"views"`
}

// Views — разметка всех включённых подразделений; у кого её нет, тех в ответе нет.
func (e *Engine) Views(ctx context.Context) ([]ServiceViews, error) {
	rows, err := e.pool.Query(ctx, `SELECT code, title, views FROM services
		WHERE enabled AND jsonb_array_length(views) > 0 ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServiceViews{}
	for rows.Next() {
		var (
			s   ServiceViews
			raw []byte
		)
		if err := rows.Scan(&s.Service, &s.Title, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &s.Views); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
