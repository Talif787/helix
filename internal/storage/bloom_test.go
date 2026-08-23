package storage

import (
	"fmt"
	"testing"
)

func TestBloomNoFalseNegatives(t *testing.T) {
	b := NewBloom(1000, 0.01)
	for i := 0; i < 1000; i++ {
		b.Add([]byte(fmt.Sprintf("present-%d", i)))
	}
	for i := 0; i < 1000; i++ {
		if !b.MayContain([]byte(fmt.Sprintf("present-%d", i))) {
			t.Fatalf("false negative for present-%d", i)
		}
	}
}

func TestBloomFalsePositiveRateIsReasonable(t *testing.T) {
	const n = 1000
	b := NewBloom(n, 0.01)
	for i := 0; i < n; i++ {
		b.Add([]byte(fmt.Sprintf("present-%d", i)))
	}

	const trials = 10000
	fp := 0
	for i := 0; i < trials; i++ {
		if b.MayContain([]byte(fmt.Sprintf("absent-%d", i))) {
			fp++
		}
	}
	rate := float64(fp) / float64(trials)
	if rate > 0.05 {
		t.Fatalf("false positive rate %.4f exceeds loose bound 0.05", rate)
	}
}

func TestBloomSerializeRoundTrip(t *testing.T) {
	b := NewBloom(500, 0.01)
	for i := 0; i < 500; i++ {
		b.Add([]byte(fmt.Sprintf("k-%d", i)))
	}
	loaded, err := LoadBloom(b.Bytes())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for i := 0; i < 500; i++ {
		if !loaded.MayContain([]byte(fmt.Sprintf("k-%d", i))) {
			t.Fatalf("round-trip lost membership for k-%d", i)
		}
	}
}

func TestBloomLoadRejectsCorrupt(t *testing.T) {
	if _, err := LoadBloom([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected error loading too-short bloom bytes")
	}
}
