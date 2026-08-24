package storage

import (
	"bytes"
	"sort"
)

// compactionSizeRatio groups SSTables into a size tier: a table joins a bucket while
// its size stays within this factor of the bucket's running average size.
const compactionSizeRatio = 1.5

// getSizeTieredBucket returns a set of similarly sized SSTables worth compacting, or
// nil when none qualifies. Tables are sorted by size and grouped greedily; the first
// group with at least minThreshold members is returned. This is a simplified form of
// the size-tiered strategy used by Cassandra.
func getSizeTieredBucket(tables []tableRef, minThreshold int) []tableRef {
	if minThreshold < 2 || len(tables) < minThreshold {
		return nil
	}
	sorted := append([]tableRef(nil), tables...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].size < sorted[j].size })

	i := 0
	for i < len(sorted) {
		bucket := []tableRef{sorted[i]}
		sum := sorted[i].size
		j := i + 1
		for j < len(sorted) {
			avg := sum / int64(len(bucket))
			if avg < 1 {
				avg = 1
			}
			if float64(sorted[j].size) <= float64(avg)*compactionSizeRatio {
				bucket = append(bucket, sorted[j])
				sum += sorted[j].size
				j++
			} else {
				break
			}
		}
		if len(bucket) >= minThreshold {
			return bucket
		}
		i = j
	}
	return nil
}

// mergeTables performs a k-way merge of the given sorted SSTables, writing the single
// highest-sequence record for each key to out and returning the number of records
// written. Because a compacted table no longer reflects data recency by file number,
// recency is resolved here by sequence number. When dropTombstones is true, tombstones
// (and thus the keys they delete) are omitted, which is only safe during a full
// compaction where no other table can hold the key.
func mergeTables(tables []*SSTable, dropTombstones bool, out *SSTableWriter) (int, error) {
	type cursor struct {
		it  *SSTableIterator
		cur Record
		ok  bool
	}
	cursors := make([]*cursor, len(tables))
	for i, st := range tables {
		it := st.Iterator()
		cur, ok := it.Next()
		cursors[i] = &cursor{it: it, cur: cur, ok: ok}
	}

	written := 0
	for {
		var minKey []byte
		any := false
		for _, c := range cursors {
			if c.ok && (!any || bytes.Compare(c.cur.Key, minKey) < 0) {
				minKey = c.cur.Key
				any = true
			}
		}
		if !any {
			break
		}

		var best Record
		haveBest := false
		for _, c := range cursors {
			if c.ok && bytes.Equal(c.cur.Key, minKey) {
				if !haveBest || c.cur.Seq > best.Seq {
					best = c.cur
					haveBest = true
				}
			}
		}
		for _, c := range cursors {
			if c.ok && bytes.Equal(c.cur.Key, minKey) {
				c.cur, c.ok = c.it.Next()
			}
		}

		if dropTombstones && best.Kind == KindDelete {
			continue
		}
		if err := out.Add(best); err != nil {
			return written, err
		}
		written++
	}

	for _, c := range cursors {
		if err := c.it.Err(); err != nil {
			return written, err
		}
	}
	return written, nil
}
