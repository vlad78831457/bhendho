package engine

// Тарифы в панели админа (ADR-69): цена бланка задачи, курсы единиц подразделения, множитель минуса.
// Меняются без перезапуска: журнал tariff_changes, действует последняя запись. Задача при создании
// запоминает цену (резерв), курсы и множитель — новые тарифы её не касаются.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"offgrid/core/internal/core/codes"
)

// overdraftFactor — множитель минуса: из тарифов, если админ его задал, иначе из настроек.
func (e *Engine) overdraftFactor(ctx context.Context, q querier) (decimal.Decimal, error) {
	var v *string
	err := q.QueryRow(ctx, `SELECT value::text FROM tariffs WHERE scope = 'overdraft_factor' AND target = ''`).Scan(&v)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return decimal.Zero, err
	}
	if v != nil {
		return decimal.RequireFromString(*v), nil
	}
	return decimal.RequireFromString(e.cfg.OverdraftFactor), nil
}

// TariffForm — цена бланка задачи: из паспорта и действующая.
type TariffForm struct {
	Service  string `json:"service"`
	FormID   string `json:"form_id"`
	Title    string `json:"title"`
	Manifest string `json:"manifest_price"`
	Price    string `json:"price"`
	Override bool   `json:"override"`
}

// TariffUnit — курс единицы подразделения: для основной — GC за единицу, для дополнительной — основных в ней.
type TariffUnit struct {
	Service  string `json:"service"`
	Unit     string `json:"unit"`
	Base     bool   `json:"base"`
	Manifest string `json:"manifest_rate"`
	Rate     string `json:"rate"`
	Override bool   `json:"override"`
}

// TariffMultiplier — коэффициент цены (ADR-78): core, <подразделение> или <подразделение>:<прораб>; по умолчанию 1.
type TariffMultiplier struct {
	Target   string `json:"target"`
	Title    string `json:"title"`
	Value    string `json:"value"`
	Override bool   `json:"override"`
}

// TariffChange — запись журнала тарифов.
type TariffChange struct {
	Scope     string    `json:"scope"`
	Target    string    `json:"target"`
	Value     *string   `json:"value"`
	Writer    string    `json:"writer"`
	CreatedAt time.Time `json:"created_at"`
}

// Tariffs — всё, что админ может поменять, и последние 50 изменений.
type Tariffs struct {
	Forms       []TariffForm       `json:"forms"`
	Units       []TariffUnit       `json:"units"`
	Multipliers []TariffMultiplier `json:"multipliers"`
	Overdraft   struct {
		Config   string `json:"config"`
		Value    string `json:"value"`
		Override bool   `json:"override"`
	} `json:"overdraft_factor"`
	History []TariffChange `json:"history"`
}

// AdminTariffs — тарифы для панели.
func (e *Engine) AdminTariffs(ctx context.Context) (Tariffs, error) {
	var out Tariffs
	over := map[string]string{}
	rows, err := e.pool.Query(ctx, `SELECT scope || '|' || target, value::text FROM tariffs WHERE value IS NOT NULL`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return out, err
		}
		over[k] = v
	}
	rows.Close()

	rows, err = e.pool.Query(ctx, `
		SELECT s.code, f.form_id, f.title, f.price::numeric(15,4)::text FROM forms f JOIN services s ON s.id = f.service_id
		WHERE f.kind = 'task' AND f.status = 'published' ORDER BY s.code, f.form_id`)
	if err != nil {
		return out, err
	}
	out.Forms = []TariffForm{}
	for rows.Next() {
		var f TariffForm
		if err := rows.Scan(&f.Service, &f.FormID, &f.Title, &f.Manifest); err != nil {
			rows.Close()
			return out, err
		}
		f.Price = f.Manifest
		if v, ok := over["form_price|"+f.FormID]; ok {
			f.Price, f.Override = decimal.RequireFromString(v).StringFixed(4), true
		}
		out.Forms = append(out.Forms, f)
	}
	rows.Close()

	svcs, err := e.AdminServices(ctx)
	if err != nil {
		return out, err
	}
	out.Units = []TariffUnit{}
	for _, s := range svcs {
		var a struct {
			BaseUnit struct {
				Name   string          `json:"name"`
				RateGC decimal.Decimal `json:"rate_gc"`
			} `json:"base_unit"`
			ExtraUnits []struct {
				Name       string          `json:"name"`
				RateToBase decimal.Decimal `json:"rate_to_base"`
			} `json:"extra_units"`
		}
		if !remarshal(s.Accounting, &a) || a.BaseUnit.Name == "" {
			continue
		}
		add := func(unit string, base bool, rate decimal.Decimal) {
			u := TariffUnit{Service: s.Code, Unit: unit, Base: base, Manifest: rate.String(), Rate: rate.String()}
			if v, ok := over["unit_rate|"+s.Code+":"+unit]; ok {
				u.Rate, u.Override = decimal.RequireFromString(v).String(), true
			}
			out.Units = append(out.Units, u)
		}
		add(a.BaseUnit.Name, true, a.BaseUnit.RateGC)
		for _, x := range a.ExtraUnits {
			add(x.Name, false, x.RateToBase)
		}
	}

	// Коэффициенты (ADR-78): ядро, подразделения, действующие прорабы.
	addK := func(target, title string) {
		m := TariffMultiplier{Target: target, Title: title, Value: "1"}
		if v, ok := over["multiplier|"+target]; ok {
			m.Value, m.Override = decimal.RequireFromString(v).String(), true
		}
		out.Multipliers = append(out.Multipliers, m)
	}
	out.Multipliers = []TariffMultiplier{}
	addK("core", "Ядро — k GC")
	for _, s := range svcs {
		addK(s.Code, s.Title)
	}
	rows, err = e.pool.Query(ctx, `SELECT s.code, a.name, COALESCE(a.title, '') FROM service_accounts a
		JOIN services s ON s.id = a.service_id WHERE a.revoked_at IS NULL ORDER BY s.code, a.name`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var code, name, title string
		if err := rows.Scan(&code, &name, &title); err != nil {
			rows.Close()
			return out, err
		}
		addK(code+":"+name, title)
	}
	rows.Close()

	out.Overdraft.Config = e.cfg.OverdraftFactor
	out.Overdraft.Value = e.cfg.OverdraftFactor
	if v, ok := over["overdraft_factor|"]; ok {
		out.Overdraft.Value, out.Overdraft.Override = decimal.RequireFromString(v).String(), true
	}

	rows, err = e.pool.Query(ctx, `SELECT scope, target, value::text, writer, created_at FROM tariff_changes ORDER BY id DESC LIMIT 50`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	out.History = []TariffChange{}
	for rows.Next() {
		var c TariffChange
		if err := rows.Scan(&c.Scope, &c.Target, &c.Value, &c.Writer, &c.CreatedAt); err != nil {
			return out, err
		}
		out.History = append(out.History, c)
	}
	return out, rows.Err()
}

// SetTariff — изменение тарифа админом; value nil — вернуть значение из паспорта или настроек.
func (e *Engine) SetTariff(ctx context.Context, adminID uuid.UUID, scope, target string, value *decimal.Decimal) error {
	bad := func(format string, a ...any) error { return refuse(codes.ValidationError, format, a...) }
	if value != nil && !value.IsPositive() {
		return bad("value must be positive")
	}
	switch scope {
	case "form_price":
		var ok bool
		if err := e.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM forms WHERE form_id = $1 AND kind = 'task' AND status = 'published')`,
			target).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return bad("%q is not a published task form", target)
		}
	case "unit_rate":
		service, unit, found := strings.Cut(target, ":")
		var ok bool
		if err := e.pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM services WHERE code = $1 AND (accounting->'base_unit'->>'name' = $2
				OR EXISTS (SELECT 1 FROM jsonb_array_elements(COALESCE(accounting->'extra_units', '[]')) u WHERE u->>'name' = $2)))`,
			service, unit).Scan(&ok); err != nil {
			return err
		}
		if !found || !ok {
			return bad("%q is not <department>:<unit> of a registered department", target)
		}
	case "overdraft_factor":
		if target != "" {
			return bad("overdraft_factor has no target")
		}
		if value != nil && value.LessThan(decimal.NewFromInt(1)) {
			return bad("overdraft_factor must be at least 1")
		}
	case "multiplier":
		// ADR-78: core — k GC; <подразделение>; <подразделение>:<прораб> — прораб с действующим ключом.
		service, prorab, isProrab := strings.Cut(target, ":")
		var ok bool
		switch {
		case target == "core":
			ok = true
		case isProrab:
			if err := e.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM service_accounts a JOIN services s ON s.id = a.service_id
				WHERE s.code = $1 AND a.name = $2 AND a.revoked_at IS NULL)`, service, prorab).Scan(&ok); err != nil {
				return err
			}
		default:
			if err := e.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM services WHERE code = $1)`, service).Scan(&ok); err != nil {
				return err
			}
		}
		if !ok {
			return bad("%q is not core, <department> or <department>:<prorab>", target)
		}
	default:
		return bad("scope is form_price, unit_rate, overdraft_factor or multiplier")
	}
	var v any
	if value != nil {
		v = value.String()
	}
	_, err := e.pool.Exec(ctx, `INSERT INTO tariff_changes (scope, target, value, writer) VALUES ($1, $2, $3::numeric, $4)`,
		scope, target, v, "admin:"+adminID.String())
	if err == nil {
		e.catalog.reset() // цена видна в каталоге сразу
	}
	return err
}

// remarshal — перекладывает значение в структуру через JSON; false — не получилось.
func remarshal(from, to any) bool {
	raw, err := json.Marshal(from)
	return err == nil && json.Unmarshal(raw, to) == nil
}

// multiplier — коэффициент цены из «Тарифов» (ADR-78): target — core, <подразделение> или <подразделение>:<прораб>;
// админ не задавал — 1.
func (e *Engine) multiplier(ctx context.Context, q querier, target string) (decimal.Decimal, error) {
	var v *string
	err := q.QueryRow(ctx, `SELECT value::text FROM tariffs WHERE scope = 'multiplier' AND target = $1`, target).Scan(&v)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return decimal.Zero, err
	}
	if v == nil {
		return decimal.NewFromInt(1), nil
	}
	return decimal.RequireFromString(*v), nil
}

// taskMultiplier — коэффициент задачи при создании: ядра × подразделения (ADR-78).
func (e *Engine) taskMultiplier(ctx context.Context, q querier, service string) (decimal.Decimal, error) {
	core, err := e.multiplier(ctx, q, "core")
	if err != nil {
		return decimal.Zero, err
	}
	dept, err := e.multiplier(ctx, q, service)
	if err != nil {
		return decimal.Zero, err
	}
	return core.Mul(dept), nil
}
