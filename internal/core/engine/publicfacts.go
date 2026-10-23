package engine

// Публичные факты (ADR-79): факт приватный (ссылается только хозяин) или публичный (ссылается любой, только
// читает). Публичным делает только админ и только свой факт — пресеты, миры, случаи студии. Снимок задачи
// закрепляет версию публичного факта так же, как своего: правки хозяина на взятые задачи не влияют.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"offgrid/core/internal/core/codes"
)

// refVisible — условие ссылки на факт f для пользователя $2: свой или публичный.
const refVisible = `(f.user_id = $2 OR f.visibility = 'public')`

// PublicFact — опубликованная версия публичного факта.
type PublicFact struct {
	ID        uuid.UUID      `json:"fact_id"`
	Kind      string         `json:"kind"`
	Version   int            `json:"version"`
	Values    map[string]any `json:"values"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// SetFactVisibility — админ делает свой факт публичным или снова приватным. Старые задачи и факты со
// ссылкой не ломаются: у задачи свой снимок; новые ссылки на приватный чужой факт не проходят.
func (e *Engine) SetFactVisibility(ctx context.Context, admin, factID uuid.UUID, visibility string) (Fact, error) {
	if visibility != "public" && visibility != "private" {
		return Fact{}, refuse(codes.ValidationError, "visibility: public or private")
	}
	tag, err := e.pool.Exec(ctx, `UPDATE facts SET visibility = $3 WHERE id = $1 AND user_id = $2`, factID, admin, visibility)
	if err != nil {
		return Fact{}, err
	}
	if tag.RowsAffected() == 0 {
		return Fact{}, refuse(codes.ValidationError, "only your own fact can change visibility")
	}
	return e.GetFact(ctx, admin, factID)
}

// PublicFacts — публичные факты вида kind (пусто — все) в опубликованной версии, новые сверху.
func (e *Engine) PublicFacts(ctx context.Context, kind string) ([]PublicFact, error) {
	rows, err := e.pool.Query(ctx, `
		SELECT f.id, f.kind, v.version, v.values, v.updated_at FROM facts f
		JOIN LATERAL (SELECT version, values, updated_at FROM fact_versions
		              WHERE fact_id = f.id AND status = 'published' ORDER BY version DESC LIMIT 1) v ON true
		WHERE f.visibility = 'public' AND ($1 = '' OR f.kind = $1)
		ORDER BY v.updated_at DESC LIMIT 500`, kind)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PublicFact, error) {
		var p PublicFact
		var raw []byte
		if err := r.Scan(&p.ID, &p.Kind, &p.Version, &raw, &p.UpdatedAt); err != nil {
			return p, err
		}
		var err error
		p.Values, err = decodeValues(raw)
		return p, err
	})
}
