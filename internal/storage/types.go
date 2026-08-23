package storage

import "encoding/binary"

// Kind distinguishes a live value from a deletion marker (tombstone). Tombstones
// are stored rather than removing the key so that, once SSTables exist in a later
// phase, a delete can shadow older on-disk versions of the same key.
type Kind uint8

const (
	// KindSet marks a record that carries a live value.
	KindSet Kind = iota + 1
	// KindDelete marks a tombstone; its value is empty.
	KindDelete
)

func (k Kind) valid() bool { return k == KindSet || k == KindDelete }

// Record is the unit of storage. Seq is a monotonically increasing sequence
// number that establishes a total order over writes for recovery and, later,
// for conflict resolution across memtable and SSTables.
type Record struct {
	Seq   uint64
	Kind  Kind
	Key   []byte
	Value []byte
}

// cloneBytes returns a defensive copy so that a caller mutating its input buffer
// after a write cannot corrupt engine state. A nil input yields a nil output.
func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

// encodeRecordPayload appends the wire encoding of r to dst and returns the
// extended slice. The framing (length prefix and checksum) is added by the WAL;
// this function encodes only the record body:
//
//	uvarint(seq) | kind(1) | uvarint(keyLen) | key | uvarint(valLen) | value
func encodeRecordPayload(dst []byte, r *Record) []byte {
	dst = binary.AppendUvarint(dst, r.Seq)
	dst = append(dst, byte(r.Kind))
	dst = binary.AppendUvarint(dst, uint64(len(r.Key)))
	dst = append(dst, r.Key...)
	dst = binary.AppendUvarint(dst, uint64(len(r.Value)))
	dst = append(dst, r.Value...)
	return dst
}

// decodeRecordPayload parses a record body produced by encodeRecordPayload. Key
// and Value are copied out of the source buffer so the caller may reuse it.
func decodeRecordPayload(data []byte) (Record, error) {
	var r Record

	seq, n := binary.Uvarint(data)
	if n <= 0 {
		return r, ErrCorruptRecord
	}
	data = data[n:]
	r.Seq = seq

	if len(data) < 1 {
		return r, ErrCorruptRecord
	}
	r.Kind = Kind(data[0])
	data = data[1:]
	if !r.Kind.valid() {
		return r, ErrCorruptRecord
	}

	klen, n := binary.Uvarint(data)
	if n <= 0 {
		return r, ErrCorruptRecord
	}
	data = data[n:]
	if uint64(len(data)) < klen {
		return r, ErrCorruptRecord
	}
	r.Key = cloneBytes(data[:klen])
	data = data[klen:]

	vlen, n := binary.Uvarint(data)
	if n <= 0 {
		return r, ErrCorruptRecord
	}
	data = data[n:]
	if uint64(len(data)) < vlen {
		return r, ErrCorruptRecord
	}
	r.Value = cloneBytes(data[:vlen])

	return r, nil
}
