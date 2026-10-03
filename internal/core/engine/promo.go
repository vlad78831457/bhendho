package engine

// Промокоды (ADR-57). Деньги принимает отдельная служба оплаты — эмитент со своим провайдером,
// юрлицом и юрисдикцией. Ядро знает только выпущенные ею коды: код — случайная строка, в БД —
// её хэш. Погашение — депозит провайдера promo:<эмитент> по контракту депозита (ADR-26):
// зачисляется ровно один раз (dedup_key promo:<id кода>, строка кода под FOR UPDATE).

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"offgrid/core/internal/core/codes"
	"offgrid/core/internal/core/db"
)

// Отказы погашения: пользователь видит разницу «нет такого / уже использован / истёк».
// Чужой персональный и отозванный код — «нет такого»: не подтверждаем, что код существует.
var (
	ErrPromoInvalid      = errors.New("promo code is not valid")
	ErrPromoUsed         = errors.New("promo code already redeemed")
	ErrPromoExpired      = errors.New("promo code expired")
	ErrPromoTooMany      = errors.New("too many wrong promo codes")
	ErrPromoConflict     = errors.New("promo code conflicts with an issued one")
	ErrPromoNotRevocable = errors.New("only an active promo code can be revoked")
)

// Алфавит кода: 32 символа без похожих 0/O и 1/I — код удобно продиктовать и переписать.
const promoAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// NewPromoCode — 16 случайных символов (80 бит) группами по 4: «K7PQ-M3XA-9FDE-TW2H».
func NewPromoCode() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	var sb strings.Builder
	for i, x := range b {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(promoAlphabet[int(x)%len(promoAlphabet)])
	}
	return sb.String(), nil
}

// NormalizePromoCode — регистр, пробелы и дефисы не важны: «k7pq m3xa…» = «K7PQ-M3XA-…».
func NormalizePromoCode(code string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(code) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func promoHash(code string) string {
	sum := sha256.Sum256([]byte("promo:" + NormalizePromoCode(code)))
	return hex.EncodeToString(sum[:])
}

// PromoIssuer — эмитент кодов (служба оплаты или оператор).
type PromoIssuer struct {
	ID   uuid.UUID
	Name string
}

// EnsurePromoIssuer создаёт или обновляет эмитента с заданным токеном (CORE_PROMO_ISSUERS).
func (e *Engine) EnsurePromoIssuer(ctx context.Context, name, token string) error {
	if len(token) < 32 {
		return fmt.Errorf("promo issuer %s: token must be at least 32 characters", name)
	}
	_, err := e.pool.Exec(ctx, `
		INSERT INTO promo_issuers (id, name, token_hash) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET token_hash = EXCLUDED.token_hash, revoked_at = NULL`,
		uuid.New(), name, hashToken(token))
	return err
}

// IssuePromoIssuerToken — новый токен эмитента (печатается один раз; прежний перестаёт работать).
func (e *Engine) IssuePromoIssuerToken(ctx context.Context, name string) (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := "pi_" + hex.EncodeToString(b[:])
	return token, e.EnsurePromoIssuer(ctx, name, token)
}

// AuthenticatePromoIssuer — эмитент по токену из Authorization.
func (e *Engine) AuthenticatePromoIssuer(ctx context.Context, token string) (PromoIssuer, error) {
	var iss PromoIssuer
	if token == "" {
		return iss, ErrNotFound
	}
	err := e.pool.QueryRow(ctx, `SELECT id, name FROM promo_issuers WHERE token_hash = $1 AND revoked_at IS NULL`,
		hashToken(token)).Scan(&iss.ID, &iss.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return iss, ErrNotFound
	}
	return iss, err
}

// PromoIssuerByName — эмитент оператора для CLI; создаётся без рабочего токена, если его нет.
func (e *Engine) PromoIssuerByName(ctx context.Context, name string) (PromoIssuer, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return PromoIssuer{}, err
	}
	iss := PromoIssuer{Name: name}
	err := e.pool.QueryRow(ctx, `
		INSERT INTO promo_issuers (id, name, token_hash) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`, uuid.New(), name, hashToken(hex.EncodeToString(b[:]))).Scan(&iss.ID)
	return iss, err
}

// PromoSpec — что эмитент просит выпустить.
type PromoSpec struct {
	Amount     decimal.Decimal
	Code       string     // свой код эмитента; пусто — ядро сгенерирует
	ExternalID string     // номер заказа: повтор запроса не выпускает второй код
	ForEmail   string     // персональный код
	ExpiresAt  *time.Time // пусто — бессрочный
}

// PromoCode — код для эмитента. Code заполнен только при выпуске: ядро его не хранит.
type PromoCode struct {
	ID         uuid.UUID  `json:"code_id"`
	Code       string     `json:"code,omitempty"`
	Amount     string     `json:"amount"`
	CurrencyID string     `json:"currency_id"`
	Status     string     `json:"status"`
	ExternalID *string    `json:"external_id"`
	ForEmail   *string    `json:"for_email"`
	ExpiresAt  *time.Time `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
	RedeemedAt *time.Time `json:"redeemed_at"`
}

const promoCols = `id, amount::text, currency_id, status, external_id, for_email, expires_at, created_at, redeemed_at`

func scanPromo(row pgx.Row) (PromoCode, error) {
	var p PromoCode
	var amount string
	err := row.Scan(&p.ID, &amount, &p.CurrencyID, &p.Status, &p.ExternalID, &p.ForEmail, &p.ExpiresAt, &p.CreatedAt, &p.RedeemedAt)
	if err == nil {
		p.Amount = decimal.RequireFromString(amount).StringFixed(4)
	}
	return p, err
}

// IssuePromo выпускает код. Повтор с тем же ExternalID возвращает уже выпущенный код
// (created=false); если код генерировало ядро, текста кода в ответе на повтор нет.
func (e *Engine) IssuePromo(ctx context.Context, iss PromoIssuer, spec PromoSpec) (PromoCode, bool, error) {
	maxAmount := decimal.RequireFromString(e.cfg.PromoMaxAmount)
	if !spec.Amount.IsPositive() || spec.Amount.GreaterThan(maxAmount) || spec.Amount.Exponent() < -4 {
		return PromoCode{}, false, refuse(codes.ValidationError, "amount must be in (0, %s] with at most 4 decimals", maxAmount)
	}
	if spec.ExpiresAt != nil && !spec.ExpiresAt.After(e.now()) {
		return PromoCode{}, false, refuse(codes.ValidationError, "expires_at must be in the future")
	}
	code := spec.Code
	if code == "" {
		var err error
		if code, err = NewPromoCode(); err != nil {
			return PromoCode{}, false, err
		}
	} else if n := len(NormalizePromoCode(code)); n < 12 || n > 64 {
		return PromoCode{}, false, refuse(codes.ValidationError, "code must have 12..64 letters and digits")
	}
	var extID, email *string
	if spec.ExternalID != "" {
		extID = &spec.ExternalID
	}
	if spec.ForEmail != "" {
		em := strings.ToLower(strings.TrimSpace(spec.ForEmail))
		email = &em
	}

	p, err := scanPromo(e.pool.QueryRow(ctx, `
		INSERT INTO promo_codes (id, issuer_id, code_hash, external_id, amount, currency_id, for_email, expires_at)
		VALUES ($1, $2, $3, $4, $5::numeric, $6, $7, $8)
		ON CONFLICT (issuer_id, external_id) DO NOTHING
		RETURNING `+promoCols,
		uuid.New(), iss.ID, promoHash(code), extID, spec.Amount.String(), e.cfg.DefaultCurrency, email, spec.ExpiresAt))
	switch {
	case err == nil:
		p.Code = code
		return p, true, nil
	case db.IsUniqueViolation(err):
		return PromoCode{}, false, ErrPromoConflict // такой код уже есть (у этого или другого эмитента)
	case !errors.Is(err, pgx.ErrNoRows):
		return PromoCode{}, false, err
	}
	// Повтор заказа: отдаём выпущенный код, если это тот же запрос.
	var hash string
	existing, err := scanPromo(e.pool.QueryRow(ctx, `SELECT `+promoCols+` FROM promo_codes WHERE issuer_id = $1 AND external_id = $2`,
		iss.ID, spec.ExternalID))
	if err != nil {
		return PromoCode{}, false, err
	}
	if err := e.pool.QueryRow(ctx, `SELECT code_hash FROM promo_codes WHERE id = $1`, existing.ID).Scan(&hash); err != nil {
		return PromoCode{}, false, err
	}
	if existing.Amount != spec.Amount.StringFixed(4) || (spec.Code != "" && hash != promoHash(spec.Code)) {
		return existing, false, ErrPromoConflict
	}
	if spec.Code != "" {
		existing.Code = spec.Code
	}
	return existing, false, nil
}

// PromoStatus — код эмитента по id.
func (e *Engine) PromoStatus(ctx context.Context, iss PromoIssuer, id uuid.UUID) (PromoCode, error) {
	p, err := scanPromo(e.pool.QueryRow(ctx, `SELECT `+promoCols+` FROM promo_codes WHERE id = $1 AND issuer_id = $2`, id, iss.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// RevokePromo — отзыв ещё не погашенного кода (возврат денег покупателю — у эмитента).
func (e *Engine) RevokePromo(ctx context.Context, iss PromoIssuer, id uuid.UUID) (PromoCode, error) {
	p, err := scanPromo(e.pool.QueryRow(ctx, `
		UPDATE promo_codes SET status = 'revoked', revoked_at = now()
		WHERE id = $1 AND issuer_id = $2 AND status = 'active' RETURNING `+promoCols, id, iss.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := e.PromoStatus(ctx, iss, id); err != nil {
			return p, err
		}
		return p, ErrPromoNotRevocable
	}
	return p, err
}

// PromoRedemption — итог погашения.
type PromoRedemption struct {
	CodeID          uuid.UUID  `json:"code_id"`
	Amount          string     `json:"amount"`
	CurrencyID      string     `json:"currency_id"`
	AlreadyRedeemed bool       `json:"already_redeemed"`
	Wallet          WalletView `json:"wallet"`
}

// RedeemPromo — пользователь вводит код. Неверные попытки считаются: после PromoMaxFailures
// за PromoFailureWindow — ErrPromoTooMany (подбор кода в 80 бит и так нереален, но не даём пробовать).
func (e *Engine) RedeemPromo(ctx context.Context, userID uuid.UUID, code string) (PromoRedemption, error) {
	var failures int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM promo_failures WHERE user_id = $1 AND at > now() - make_interval(secs => $2)`,
		userID, e.cfg.PromoFailureWindow.Seconds()).Scan(&failures); err != nil {
		return PromoRedemption{}, err
	}
	if failures >= e.cfg.PromoMaxFailures {
		return PromoRedemption{}, ErrPromoTooMany
	}
	res, err := e.redeem(ctx, userID, code, nil)
	if errors.Is(err, ErrPromoInvalid) || errors.Is(err, ErrPromoUsed) || errors.Is(err, ErrPromoExpired) {
		_, _ = e.pool.Exec(ctx, `INSERT INTO promo_failures (user_id) VALUES ($1)`, userID)
		_, _ = e.pool.Exec(ctx, `DELETE FROM promo_failures WHERE user_id = $1 AND at < now() - interval '1 day'`, userID)
	}
	return res, err
}

// RedeemPromoFor — вебхук эмитента «код оплачен, зачислите пользователю с этим email».
// Эмитент гасит только свои коды.
func (e *Engine) RedeemPromoFor(ctx context.Context, iss PromoIssuer, email, code string) (PromoRedemption, error) {
	var userID uuid.UUID
	err := e.pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, strings.ToLower(strings.TrimSpace(email))).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return PromoRedemption{}, fmt.Errorf("user %q: %w", email, ErrNotFound)
	}
	if err != nil {
		return PromoRedemption{}, err
	}
	return e.redeem(ctx, userID, code, &iss.ID)
}

func (e *Engine) redeem(ctx context.Context, userID uuid.UUID, code string, issuerID *uuid.UUID) (PromoRedemption, error) {
	var res PromoRedemption
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var (
			id, codeIssuer   uuid.UUID
			amount, currency string
			status, issuer   string
			forEmail         *string
			expiresAt        *time.Time
			redeemedBy       *uuid.UUID
			userEmail        string
		)
		err := tx.QueryRow(ctx, `
			SELECT c.id, c.issuer_id, i.name, c.amount::text, c.currency_id, c.status, c.for_email, c.expires_at, c.redeemed_by
			FROM promo_codes c JOIN promo_issuers i ON i.id = c.issuer_id
			WHERE c.code_hash = $1 FOR UPDATE OF c`, promoHash(code)).
			Scan(&id, &codeIssuer, &issuer, &amount, &currency, &status, &forEmail, &expiresAt, &redeemedBy)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPromoInvalid
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(email, '') FROM users WHERE id = $1`, userID).Scan(&userEmail); err != nil {
			return err
		}
		if (issuerID != nil && *issuerID != codeIssuer) || status == "revoked" || (forEmail != nil && *forEmail != userEmail) {
			return ErrPromoInvalid
		}
		res = PromoRedemption{CodeID: id, Amount: decimal.RequireFromString(amount).StringFixed(4), CurrencyID: currency}
		if status == "redeemed" {
			if redeemedBy != nil && *redeemedBy == userID {
				// Повтор того же пользователя (двойной клик, повтор вебхука) — деньги уже зачислены.
				res.AlreadyRedeemed = true
				ws, err := listWallets(ctx, tx, userID)
				for _, w := range ws {
					if w.CurrencyID == currency {
						res.Wallet = w
					}
				}
				return err
			}
			return ErrPromoUsed
		}
		if expiresAt != nil && !expiresAt.After(e.now()) {
			return ErrPromoExpired
		}
		view, depositID, err := creditDeposit(ctx, tx, userID, currency, decimal.RequireFromString(amount),
			"promo:"+issuer, "promo:"+id.String())
		if err != nil {
			return err
		}
		res.Wallet = view
		_, err = tx.Exec(ctx, `UPDATE promo_codes SET status = 'redeemed', redeemed_by = $2, redeemed_at = now(), deposit_id = $3
			WHERE id = $1`, id, userID, depositID)
		return err
	})
	return res, err
}
