package core

import (
	"crypto/sha1"
	"encoding/hex"
)

/**
 * Takes a buffer of data and returns the SHA1 sum of the data
 *
 * @param data The data that is being checksummed
 */
func Hash(data []byte) string {
	sha1bytes := sha1.Sum(data)
	return hex.EncodeToString(sha1bytes[:])
}
