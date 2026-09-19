// Package nmhash produces the short deterministic hashes that make nm's
// directory and branch names unique without being unreadable.
package nmhash

import (
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

// DefaultLength is the hash length used when configuration does not say.
const DefaultLength = 6

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Short hashes parts into a lowercase alphanumeric string of n characters.
// The same parts always produce the same hash, and the result is safe in both
// filesystem paths and git branch names.
func Short(n int, parts ...string) string {
	if n <= 0 {
		n = DefaultLength
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	encoded := strings.ToLower(encoding.EncodeToString(sum[:]))
	if n > len(encoded) {
		n = len(encoded)
	}
	return encoded[:n]
}
