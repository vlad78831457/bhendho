package engine

// Выгрузка всей базы за период (ADR-67): ZIP, по файлу JSON Lines на таблицу. Таблицы берутся из самой базы —
// новые попадают в выгрузку сами. Строки отбираются по created_at (или at) в [from, to); таблицы без времени
// (справочники, кошельки) — целиком. Секреты не выгружаются: хэши паролей, токенов, кодов и меток, служебные
// таблицы входа.

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// exportSkip — таблицы, которых в выгрузке нет: сессии, одноразовые коды входа, метки пополнения, миграции.
var exportSkip = []string{"refresh_tokens", "oauth_states", "oauth_logins", "topup_marks", "schema_migrations"}

// exportSecret — столбцы-секреты: вырезаются из каждой строки.
func exportSecret(col string) bool {
	return strings.HasSuffix(col, "_hash") || col == "verifier" || col == "nonce"
}

// ExportTable — что выгружено из таблицы.
type ExportTable struct {
	Rows     int    `json:"rows"`
	ByPeriod string `json:"by_period,omitempty"` // столбец времени; пусто — таблица целиком
}

// ExportDB пишет архив в w. Одна транзакция «только чтение» со снимком: таблицы согласованы между собой.
func (e *Engine) ExportDB(ctx context.Context, w io.Writer, from, to time.Time) error {
	zw := zip.NewWriter(w)
	tables := map[string]ExportTable{}
	err := pgx.BeginTxFunc(ctx, e.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		names, err := exportTables(ctx, tx)
		if err != nil {
			return err
		}
		for _, name := range names {
			info, err := exportTable(ctx, tx, zw, name, from, to)
			if err != nil {
				return fmt.Errorf("export %s: %w", name, err)
			}
			tables[name] = info
		}
		return nil
	})
	if err != nil {
		return err
	}
	mf, err := zw.Create("manifest.json")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(mf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{
		"from": from.UTC(), "to": to.UTC(), "generated_at": e.now().UTC(), "tables": tables,
		"note": "строки — JSON Lines; секреты (хэши паролей, токенов, кодов) не выгружаются",
	}); err != nil {
		return err
	}
	return zw.Close()
}

func exportTables(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE' ORDER BY table_name`)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(names, func(n string) bool { return slices.Contains(exportSkip, n) }), nil
}

func exportTable(ctx context.Context, tx pgx.Tx, zw *zip.Writer, name string, from, to time.Time) (ExportTable, error) {
	rows, err := tx.Query(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1`, name)
	if err != nil {
		return ExportTable{}, err
	}
	cols, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return ExportTable{}, err
	}
	var info ExportTable
	for _, c := range []string{"created_at", "at"} {
		if slices.Contains(cols, c) {
			info.ByPeriod = c
			break
		}
	}
	q := "SELECT to_jsonb(t)"
	for _, c := range cols {
		if exportSecret(c) {
			q += " - " + quoteLiteral(c)
		}
	}
	q += " FROM " + pgx.Identifier{name}.Sanitize() + " t"
	var args []any
	if info.ByPeriod != "" {
		col := pgx.Identifier{info.ByPeriod}.Sanitize()
		q += fmt.Sprintf(" WHERE t.%s >= $1 AND t.%s < $2 ORDER BY t.%s", col, col, col)
		args = []any{from, to}
	}
	out, err := zw.Create(name + ".jsonl")
	if err != nil {
		return info, err
	}
	data, err := tx.Query(ctx, q, args...)
	if err != nil {
		return info, err
	}
	defer data.Close()
	for data.Next() {
		var raw []byte
		if err := data.Scan(&raw); err != nil {
			return info, err
		}
		if _, err := out.Write(append(raw, '\n')); err != nil {
			return info, err
		}
		info.Rows++
	}
	return info, data.Err()
}

// quoteLiteral — имя столбца как строковый литерал SQL (для оператора jsonb «-»).
func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
