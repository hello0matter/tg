package id

import (
	"crypto/rand"
	"encoding/hex"
)

func New() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
