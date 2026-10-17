package engine

// Метки пополнения (ADR-62): служба оплаты зачисляет код пользователю по одноразовой метке, а не по email —
// у вошедших через VK email может не быть. Метку выдаёт ядро вошедшему пользователю; в базе — только хэш.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	// ErrMarkInvalid — такой метки нет.
	ErrMarkInvalid = errors.New("top-up mark is not valid")
	// ErrMarkUsed — по метке уже погашен другой код.
	ErrMarkUsed = errors.New("top-up mark already used for another code")
	// ErrMarkExpired — срок метки истёк, а код по ней ещё не гасили.
	ErrMarkExpired = errors.New("top-up mark expired")
)

const markPrefix = "tm_"

// TopupMark — метка для кнопки «Пополнить». Mark показывается один раз.
type TopupMark struct {
	Mark      string    `json:"mark"`
	ExpiresAt time.Time `json:"expires_at"`
}

func markHash(mark string) string {
	sum := sha256.Sum256([]byte("topup:" + strings.TrimSpace(mark)))
	return hex.EncodeToString(sum[:])
}

// IssueTopupMark выдаёт пользователю новую метку со сроком CORE_TOPUP_MARK_TTL.
func (e *Engine) IssueTopupMark(ctx context.Context, userID uuid.UUID) (TopupMark, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return TopupMark{}, err
	}
	m := TopupMark{Mark: markPrefix + base64.RawURLEncoding.EncodeToString(buf), ExpiresAt: e.now().Add(e.cfg.TopupMarkTTL)}
	_, err := e.pool.Exec(ctx, `INSERT INTO topup_marks (id, user_id, mark_hash, expires_at) VALUES ($1, $2, $3, $4)`,
		uuid.New(), userID, markHash(m.Mark), m.ExpiresAt)
	return m, err
}

// RedeemPromoByMark — вебхук службы оплаты: погасить код пользователю, чью метку принёс покупатель.
// Метка и код проверяются одной транзакцией: второй код по той же метке откатывает погашение.
func (e *Engine) RedeemPromoByMark(ctx context.Context, iss PromoIssuer, mark, code string) (PromoRedemption, error) {
	var res PromoRedemption
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var (
			markID, userID uuid.UUID
			expiresAt      time.Time
			boundCode      *uuid.UUID
		)
		err := tx.QueryRow(ctx, `SELECT id, user_id, expires_at, code_id FROM topup_marks WHERE mark_hash = $1 FOR UPDATE`,
			markHash(mark)).Scan(&markID, &userID, &expiresAt, &boundCode)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMarkInvalid
		}
		if err != nil {
			return err
		}
		if boundCode == nil && !expiresAt.After(e.now()) {
			return ErrMarkExpired
		}
		if res, err = e.redeemTx(ctx, tx, userID, code, &iss.ID); err != nil {
			return err
		}
		switch {
		case boundCode == nil:
			_, err = tx.Exec(ctx, `UPDATE topup_marks SET code_id = $2, used_at = now() WHERE id = $1`, markID, res.CodeID)
			return err
		case *boundCode != res.CodeID:
			return ErrMarkUsed
		}
		return nil
	})
	return res, err
}

// MarkOwner — чей счёт пополняет метка: для страницы оплаты, чтобы подсунутую чужую ссылку было видно
// (разбор безопасности 6.2). Только замаскированный email или имя, без номера пользователя.
type MarkOwner struct {
	Account   string    `json:"account"`
	ExpiresAt time.Time `json:"expires_at"`
}

// TopupMarkOwner — владелец действующей неиспользованной метки.
func (e *Engine) TopupMarkOwner(ctx context.Context, mark string) (MarkOwner, error) {
	var (
		out         MarkOwner
		email, name string
	)
	err := e.pool.QueryRow(ctx, `SELECT COALESCE(u.email, ''), u.display_name, m.expires_at
		FROM topup_marks m JOIN users u ON u.id = m.user_id
		WHERE m.mark_hash = $1 AND m.code_id IS NULL AND m.expires_at > now()`, markHash(mark)).
		Scan(&email, &name, &out.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrMarkInvalid
	}
	if err != nil {
		return out, err
	}
	out.Account = maskAccount(email, name)
	return out, nil
}

// maskAccount: «vlad@mail.ru» → «v***@mail.ru»; без email — имя: «Иван Петров» → «И*** П***».
func maskAccount(email, name string) string {
	mask := func(s string) string {
		r := []rune(s)
		if len(r) == 0 {
			return ""
		}
		return string(r[0]) + "***"
	}
	if local, domain, ok := strings.Cut(email, "@"); ok {
		return mask(local) + "@" + domain
	}
	parts := strings.Fields(name)
	for i := range parts {
		parts[i] = mask(parts[i])
	}
	if len(parts) == 0 {
		return "без имени"
	}
	return strings.Join(parts, " ")
}
