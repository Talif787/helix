package storage

import (
	"encoding/binary"
	"hash/fnv"
	"math"
)

// Bloom is a classic bloom filter used to answer "is this key definitely absent?"
// without a disk read. It never reports a false negative: if Add(k) was called,
// MayContain(k) is always true. It may report a false positive, at a rate bounded
// by the parameters given to NewBloom.
//
// Two 32-bit hashes are derived from one 64-bit FNV-1a hash and combined with the
// standard double-hashing scheme g_i = h1 + i*h2, which approximates i independent
// hash functions well enough for this purpose.
type Bloom struct {
	bits []byte
	m    uint32 // number of bits (a multiple of 8)
	k    uint32 // number of hash functions
}

// NewBloom sizes a filter for about n items at a target false-positive rate p.
func NewBloom(n int, p float64) *Bloom {
	if n < 1 {
		n = 1
	}
	if p <= 0 || p >= 1 {
		p = 0.01
	}
	m := uint32(math.Ceil(-(float64(n) * math.Log(p)) / (math.Ln2 * math.Ln2)))
	if m < 8 {
		m = 8
	}
	m = (m + 7) &^ 7 // round up to a whole number of bytes

	k := uint32(math.Round(float64(m) / float64(n) * math.Ln2))
	if k < 1 {
		k = 1
	}
	return &Bloom{bits: make([]byte, m/8), m: m, k: k}
}

func bloomHashes(key []byte) (uint32, uint32) {
	h := fnv.New64a()
	_, _ = h.Write(key)
	sum := h.Sum64()
	h1 := uint32(sum)
	h2 := uint32(sum >> 32)
	if h2 == 0 {
		h2 = 1 // keep the step non-zero so probes spread across the filter
	}
	return h1, h2
}

// Add records key in the filter.
func (b *Bloom) Add(key []byte) {
	h1, h2 := bloomHashes(key)
	for i := uint32(0); i < b.k; i++ {
		idx := (h1 + i*h2) % b.m
		b.bits[idx>>3] |= 1 << (idx & 7)
	}
}

// MayContain reports whether key might be present. A false result is definitive;
// a true result means "present, or a false positive".
func (b *Bloom) MayContain(key []byte) bool {
	h1, h2 := bloomHashes(key)
	for i := uint32(0); i < b.k; i++ {
		idx := (h1 + i*h2) % b.m
		if b.bits[idx>>3]&(1<<(idx&7)) == 0 {
			return false
		}
	}
	return true
}

// Bytes serializes the filter as m(uint32 LE) | k(uint32 LE) | bits, for embedding
// in an SSTable.
func (b *Bloom) Bytes() []byte {
	out := make([]byte, 8, 8+len(b.bits))
	binary.LittleEndian.PutUint32(out[0:4], b.m)
	binary.LittleEndian.PutUint32(out[4:8], b.k)
	out = append(out, b.bits...)
	return out
}

// LoadBloom reconstructs a filter produced by Bytes.
func LoadBloom(data []byte) (*Bloom, error) {
	if len(data) < 8 {
		return nil, ErrCorruptRecord
	}
	m := binary.LittleEndian.Uint32(data[0:4])
	k := binary.LittleEndian.Uint32(data[4:8])
	bits := data[8:]
	if uint32(len(bits)) != m/8 || k == 0 {
		return nil, ErrCorruptRecord
	}
	return &Bloom{bits: cloneBytes(bits), m: m, k: k}, nil
}
