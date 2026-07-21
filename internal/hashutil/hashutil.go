// Package hashutil computes and compares SHA256 file digests. CivitAI publishes
// SHA256 (uppercase hex) per file; downloads are verified against it.
package hashutil

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
)

// SumFile returns the lowercase-hex SHA256 of the file at path.
func SumFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Equal compares two hex digests case-insensitively (the API returns uppercase;
// crypto/sha256 emits lowercase). Empty inputs never match.
func Equal(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(a, b)
}

// FileMatches reports whether the file at path exists and hashes to the given
// expected SHA256. A missing file or read error reports false.
func FileMatches(path, expectedSHA256 string) bool {
	if expectedSHA256 == "" {
		return false
	}
	sum, err := SumFile(path)
	if err != nil {
		return false
	}
	return Equal(sum, expectedSHA256)
}
