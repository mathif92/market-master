// Package idgen produces random, collision-resistant identifiers
// (UUID-v4 shaped, crypto/rand based) for entities whose id must exist
// before the database row does (e.g. reserving stock for an order id).
package idgen

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns a canonical UUID v4 string (lowercase, dashed).
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
