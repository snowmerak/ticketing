package queue

import "testing"

func TestRedisBitOrderAndPageBoundaries(t *testing.T) {
	const pageBits = uint64(65536)
	for _, seq := range []uint64{0, 7, 8, pageBits - 1, pageBits, pageBits + 1} {
		page, offset := PageOffset(seq, pageBits)
		if page != seq/pageBits || offset != seq%pageBits {
			t.Fatalf("PageOffset(%d) = (%d,%d)", seq, page, offset)
		}
		raw := make([]byte, pageBits/8)
		SetRedisBit(raw, offset)
		if !RedisBitSet(raw, offset) {
			t.Fatalf("offset %d was not set", offset)
		}
		if offset == 0 && raw[0] != 0x80 {
			t.Fatalf("redis offset 0 byte = %#x, want 0x80", raw[0])
		}
	}
}

func TestEligibleORsSlotsAndRemovesBlocked(t *testing.T) {
	ready := [][]byte{{0x80}, {0x80, 0x40}}
	got := Eligible(ready, []byte{0x00}, []byte{0x80})
	if len(got) != 2 || got[0] != 0 || got[1] != 0x40 || CountBits(got) != 1 {
		t.Fatalf("Eligible() = %#v", got)
	}
}

func TestSetOffsetsUsesRedisMSBOrder(t *testing.T) {
	got := SetOffsets([]byte{0x81, 0x40}, 0)
	want := []uint64{0, 7, 9}
	if len(got) != len(want) {
		t.Fatalf("offsets = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("offsets = %v, want %v", got, want)
		}
	}
}
