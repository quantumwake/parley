package event

import (
	"crypto/rand"
	"crypto/sha256"
	"time"
)

// ULIDs are the event ids: 48 bits of millisecond time and 80 bits of
// randomness in Crockford base32, sortable by creation time and unique
// without coordination. This is a self-contained encoder so the library
// carries no dependency for it.

const ulidLen = 26

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var crockfordIndex = func() [256]int8 {
	var idx [256]int8
	for i := range idx {
		idx[i] = -1
	}

	for i, c := range crockford {
		idx[c] = int8(i)
		if c >= 'A' && c <= 'Z' {
			idx[c+('a'-'A')] = int8(i)
		}
	}

	return idx
}()

// NewID returns a ULID for now.
func NewID() string {
	return NewIDAt(time.Now())
}

// NewIDAt returns a ULID whose time component is t; used by tests and by
// the transcript tailer, which stamps rows with the transcript's time.
func NewIDAt(t time.Time) string {
	var raw [16]byte
	ms := uint64(t.UnixMilli())
	raw[0] = byte(ms >> 40)
	raw[1] = byte(ms >> 32)
	raw[2] = byte(ms >> 24)
	raw[3] = byte(ms >> 16)
	raw[4] = byte(ms >> 8)
	raw[5] = byte(ms)
	if _, err := rand.Read(raw[6:]); err != nil {
		panic("event: crypto/rand unavailable: " + err.Error())
	}

	return encodeULID(raw)
}

// DeriveID returns a ULID whose time is t and whose random half is a
// digest of key, so the same (t, key) always yields the same id. Capture
// uses it for rows re-derived from a source that can be re-read (a
// transcript block, a tool call id), which makes re-tailing idempotent.
func DeriveID(t time.Time, key string) string {
	var raw [16]byte
	ms := uint64(t.UnixMilli())
	raw[0] = byte(ms >> 40)
	raw[1] = byte(ms >> 32)
	raw[2] = byte(ms >> 24)
	raw[3] = byte(ms >> 16)
	raw[4] = byte(ms >> 8)
	raw[5] = byte(ms)
	sum := sha256.Sum256([]byte(key))
	copy(raw[6:], sum[:10])
	return encodeULID(raw)
}

// encodeULID writes 128 bits as 26 base32 characters (the first carries 3 bits).
func encodeULID(raw [16]byte) string {
	var out [ulidLen]byte
	out[0] = crockford[(raw[0]&224)>>5]
	out[1] = crockford[raw[0]&31]
	out[2] = crockford[(raw[1]&248)>>3]
	out[3] = crockford[((raw[1]&7)<<2)|((raw[2]&192)>>6)]
	out[4] = crockford[(raw[2]&62)>>1]
	out[5] = crockford[((raw[2]&1)<<4)|((raw[3]&240)>>4)]
	out[6] = crockford[((raw[3]&15)<<1)|((raw[4]&128)>>7)]
	out[7] = crockford[(raw[4]&124)>>2]
	out[8] = crockford[((raw[4]&3)<<3)|((raw[5]&224)>>5)]
	out[9] = crockford[raw[5]&31]
	out[10] = crockford[(raw[6]&248)>>3]
	out[11] = crockford[((raw[6]&7)<<2)|((raw[7]&192)>>6)]
	out[12] = crockford[(raw[7]&62)>>1]
	out[13] = crockford[((raw[7]&1)<<4)|((raw[8]&240)>>4)]
	out[14] = crockford[((raw[8]&15)<<1)|((raw[9]&128)>>7)]
	out[15] = crockford[(raw[9]&124)>>2]
	out[16] = crockford[((raw[9]&3)<<3)|((raw[10]&224)>>5)]
	out[17] = crockford[raw[10]&31]
	out[18] = crockford[(raw[11]&248)>>3]
	out[19] = crockford[((raw[11]&7)<<2)|((raw[12]&192)>>6)]
	out[20] = crockford[(raw[12]&62)>>1]
	out[21] = crockford[((raw[12]&1)<<4)|((raw[13]&240)>>4)]
	out[22] = crockford[((raw[13]&15)<<1)|((raw[14]&128)>>7)]
	out[23] = crockford[(raw[14]&124)>>2]
	out[24] = crockford[((raw[14]&3)<<3)|((raw[15]&224)>>5)]
	out[25] = crockford[raw[15]&31]

	return string(out[:])
}

// IsULID reports whether s is 26 Crockford base32 characters with a valid
// first character (the 128-bit value must fit: first char <= '7').
func IsULID(s string) bool {
	if len(s) != ulidLen {
		return false
	}

	if crockfordIndex[s[0]] < 0 || crockfordIndex[s[0]] > 7 {
		return false
	}

	for i := 1; i < ulidLen; i++ {
		if crockfordIndex[s[i]] < 0 {
			return false
		}
	}

	return true
}

// ULIDTime extracts the millisecond timestamp of a ULID; zero if invalid.
func ULIDTime(s string) time.Time {
	if !IsULID(s) {
		return time.Time{}
	}

	var ms uint64
	for i := 0; i < 10; i++ {
		ms = ms<<5 | uint64(crockfordIndex[s[i]])
	}

	return time.UnixMilli(int64(ms))
}
