package guild

import (
	"crypto/rand"
	"encoding/base32"
)

// randomCode даёт код приглашения вида "K3M7QW2A".
// base32 без цифр 0/1 и букв O/I — код проще диктовать голосом.
func randomCode() (string, error) {
	buf := make([]byte, 5) // 40 бит → 8 символов base32
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)[:8], nil
}
