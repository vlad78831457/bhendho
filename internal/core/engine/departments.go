package engine

// Подразделения подключаются сами (ADR-70). Админ выдаёт ключ на код подразделения; подразделение при старте
// присылает паспорт и формы — ядро проверяет их по контрактам и регистрирует без перезапуска. Тот же ключ —
// токен прораба этого подразделения. Выключенное подразделение не видно в каталоге и не получает задач;
// факты остаются.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"offgrid/core/internal/core/codes"
	"offgrid/core/internal/core/contracts"
	"offgrid/core/internal/core/modules"
)

// WithContracts включает регистрацию подразделений по сети: паспорт и формы проверяются этими схемами.
func (e *Engine) WithContracts(v *contracts.Validator) *Engine {
	e.schemas = v
	return e
}

var departmentCode = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,40}$`)

// ErrForeignDepartment — ключ выдан на другое подразделение.
var ErrForeignDepartment = errors.New("the key belongs to another department")

// IssueDepartmentKey — ключ подразделения; показывается один раз, в базе — хэш. Повторная выдача на те же
// код и имя заменяет ключ: старый перестаёт работать.
func (e *Engine) IssueDepartmentKey(ctx context.Context, adminID uuid.UUID, code, name string) (string, error) {
	if !departmentCode.MatchString(code) || !departmentCode.MatchString(name) {
		return "", refuse(codes.ValidationError, "code and name: lowercase latin, digits, - and _, 2–41 characters")
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := "dk_" + hex.EncodeToString(b[:])
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO department_keys (id, code, name, token_hash, created_by) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (code, name) DO UPDATE SET token_hash = EXCLUDED.token_hash, created_by = EXCLUDED.created_by,
				created_at = now(), revoked_at = NULL`,
			uuid.New(), code, name, hashToken(token), "admin:"+adminID.String()); err != nil {
			return err
		}
		// Подразделение уже зарегистрировано — прораб с этим именем сразу получает новый ключ.
		_, err := tx.Exec(ctx, `
			UPDATE service_accounts SET token_hash = $3, revoked_at = NULL
			WHERE name = $2 AND service_id = (SELECT id FROM services WHERE code = $1)`, code, name, hashToken(token))
		return err
	})
	return token, err
}

// Registered — что ядро приняло от подразделения.
type Registered struct {
	Service string   `json:"service"`
	Module  string   `json:"module"`
	Version string   `json:"version"`
	Forms   []string `json:"forms"`
}

// RegisterDepartment — подразделение присылает паспорт и формы со своим ключом.
// ProrabInfo — что прораб сообщает о себе при подключении (ADR-77).
type ProrabInfo struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (e *Engine) RegisterDepartment(ctx context.Context, token string, manifest []byte, files map[string][]byte, info ProrabInfo) (Registered, error) {
	if e.schemas == nil {
		return Registered{}, errors.New("department registration is off: no contracts")
	}
	var code, name string
	err := e.pool.QueryRow(ctx, `SELECT code, name FROM department_keys WHERE token_hash = $1 AND revoked_at IS NULL`,
		hashToken(token)).Scan(&code, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return Registered{}, ErrNotFound
	}
	if err != nil {
		return Registered{}, err
	}
	mod, err := modules.Parse(code, manifest, files, e.schemas)
	if err != nil {
		return Registered{}, refuse(codes.ValidationError, "%v", err)
	}
	if mod.TargetService() != code {
		return Registered{}, fmt.Errorf("%w: key for %q, passport for %q", ErrForeignDepartment, code, mod.TargetService())
	}
	if err := e.RegisterModule(ctx, mod); err != nil {
		return Registered{}, err
	}
	if _, err := e.pool.Exec(ctx, `
		INSERT INTO service_accounts (id, service_id, name, token_hash, title, description, last_seen_at)
		SELECT $1, id, $3, $4, $5, $6, now() FROM services WHERE code = $2
		ON CONFLICT (service_id, name) DO UPDATE SET token_hash = EXCLUDED.token_hash, revoked_at = NULL,
			title = EXCLUDED.title, description = EXCLUDED.description, last_seen_at = now()`,
		uuid.New(), code, name, hashToken(token), clip(strings.TrimSpace(info.Title), 80), clip(strings.TrimSpace(info.Description), 500)); err != nil {
		return Registered{}, err
	}
	out := Registered{Service: code, Module: mod.Manifest.ModuleID, Version: mod.Manifest.Version, Forms: []string{}}
	for _, f := range mod.Forms {
		out.Forms = append(out.Forms, f.Ref.FormID)
	}
	return out, nil
}

// SetServiceEnabled — выключатель подразделения.
func (e *Engine) SetServiceEnabled(ctx context.Context, code string, enabled bool) error {
	tag, err := e.pool.Exec(ctx, `UPDATE services SET enabled = $2, updated_at = now() WHERE code = $1`, code, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	e.catalog.reset()
	return nil
}

// DepartmentKey — выданный ключ: подключался ли по нему прораб (ADR-77).
type DepartmentKey struct {
	Code       string     `json:"code"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	Connected  bool       `json:"connected"`
	Title      string     `json:"title"`
	LastSeenAt *time.Time `json:"last_seen_at"`
}

// DepartmentKeys — все действующие ключи и что известно о прорабах по ним.
func (e *Engine) DepartmentKeys(ctx context.Context) ([]DepartmentKey, error) {
	rows, err := e.pool.Query(ctx, `
		SELECT k.code, k.name, k.created_at, a.id IS NOT NULL, COALESCE(a.title, ''), a.last_seen_at
		FROM department_keys k
		LEFT JOIN services s ON s.code = k.code
		LEFT JOIN service_accounts a ON a.service_id = s.id AND a.name = k.name AND a.token_hash = k.token_hash
		WHERE k.revoked_at IS NULL ORDER BY k.code, k.name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DepartmentKey, error) {
		var k DepartmentKey
		return k, r.Scan(&k.Code, &k.Name, &k.CreatedAt, &k.Connected, &k.Title, &k.LastSeenAt)
	})
}
