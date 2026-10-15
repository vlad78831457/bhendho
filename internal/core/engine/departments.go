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
func (e *Engine) RegisterDepartment(ctx context.Context, token string, manifest []byte, files map[string][]byte) (Registered, error) {
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
		INSERT INTO service_accounts (id, service_id, name, token_hash)
		SELECT $1, id, $3, $4 FROM services WHERE code = $2
		ON CONFLICT (service_id, name) DO UPDATE SET token_hash = EXCLUDED.token_hash, revoked_at = NULL`,
		uuid.New(), code, name, hashToken(token)); err != nil {
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
