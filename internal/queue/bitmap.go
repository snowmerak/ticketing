package queue

import "math/bits"

func PageOffset(seq, pageBits uint64) (page, offset uint64) {
	return seq / pageBits, seq % pageBits
}

func Slot(nowMS, slotWidthMS int64) int64 {
	return nowMS / slotWidthMS
}

func Slots(current, count int64) []int64 {
	result := make([]int64, 0, count)
	for slot := current - count + 1; slot <= current; slot++ {
		if slot >= 0 {
			result = append(result, slot)
		}
	}
	return result
}

func RedisBitSet(raw []byte, offset uint64) bool {
	index := offset / 8
	if index >= uint64(len(raw)) {
		return false
	}
	mask := byte(1 << (7 - (offset % 8)))
	return raw[index]&mask != 0
}

func SetRedisBit(raw []byte, offset uint64) {
	index := offset / 8
	if index >= uint64(len(raw)) {
		return
	}
	raw[index] |= byte(1 << (7 - (offset % 8)))
}

func Eligible(ready [][]byte, spent, reserved []byte) []byte {
	length := len(spent)
	if len(reserved) > length {
		length = len(reserved)
	}
	for _, item := range ready {
		if len(item) > length {
			length = len(item)
		}
	}
	result := make([]byte, length)
	for _, item := range ready {
		for i, value := range item {
			result[i] |= value
		}
	}
	for i := range result {
		var blocked byte
		if i < len(spent) {
			blocked |= spent[i]
		}
		if i < len(reserved) {
			blocked |= reserved[i]
		}
		result[i] &^= blocked
	}
	return result
}

func CountBits(raw []byte) int {
	total := 0
	for _, value := range raw {
		total += bits.OnesCount8(value)
	}
	return total
}

func SetOffsets(raw []byte, limit int) []uint64 {
	result := make([]uint64, 0, limit)
	for byteIndex, value := range raw {
		for value != 0 && (limit <= 0 || len(result) < limit) {
			leading := bits.LeadingZeros8(value)
			result = append(result, uint64(byteIndex*8+leading))
			value &^= 1 << (7 - leading)
		}
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result
}
