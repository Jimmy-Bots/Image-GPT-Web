package register

import (
	"crypto/sha256"
	"encoding/base64"
)

func generatePKCE(src RandomSource) (string, string) {
	buf := make([]byte, 64)
	if _, err := src.Read(buf); err != nil {
		panic(err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}
