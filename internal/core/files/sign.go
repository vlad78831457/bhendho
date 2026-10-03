package files

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"time"
)

// Signer — подписанные ссылки на файл: картинку в <img> браузер грузит без заголовка
// Authorization, поэтому доступ даёт подпись с коротким сроком, а не токен.
type Signer struct{ key []byte }

// NewSigner — ключ подписи выводится из секрета ядра (отдельно от ключа JWT).
func NewSigner(secret []byte) Signer {
	sum := sha256.Sum256(append([]byte("offgrid-file-links:"), secret...))
	return Signer{key: sum[:]}
}

func (s Signer) mac(id string, exp int64) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(id + "|" + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Sign — срок (unix-секунды) и подпись для файла.
func (s Signer) Sign(id string, until time.Time) (exp int64, sig string) {
	exp = until.Unix()
	return exp, s.mac(id, exp)
}

// Valid — подпись верна и срок не вышел.
func (s Signer) Valid(id, expRaw, sig string, now time.Time) bool {
	exp, err := strconv.ParseInt(expRaw, 10, 64)
	if err != nil || now.Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(s.mac(id, exp)))
}
