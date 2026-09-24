package queue

import "testing"

func BenchmarkEligibleDensePage(b *testing.B) {
	ready := make([]byte, 8192)
	spent := make([]byte, 8192)
	reserved := make([]byte, 8192)
	for index := range ready {
		ready[index] = 0xff
		spent[index] = byte(index)
		reserved[index] = byte(index >> 1)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(ready) * 3))
	for b.Loop() {
		result := Eligible([][]byte{ready}, spent, reserved)
		if len(result) != len(ready) {
			b.Fatal("unexpected result length")
		}
	}
}

func BenchmarkSetOffsetsDensePage(b *testing.B) {
	bitmap := make([]byte, 8192)
	for index := range bitmap {
		bitmap[index] = 0xff
	}
	b.ReportAllocs()
	for b.Loop() {
		result := SetOffsets(bitmap, 25)
		if len(result) != 25 {
			b.Fatal("unexpected result length")
		}
	}
}
