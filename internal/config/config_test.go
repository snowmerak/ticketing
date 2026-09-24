package config

import (
	"testing"
	"time"
)

func TestReadySlotsIncludesBoundarySlot(t *testing.T) {
	cfg := Config{ReadyWindow: 10 * time.Second, SlotWidth: 2 * time.Second}
	if got := cfg.ReadySlots(); got != 6 {
		t.Fatalf("ReadySlots() = %d, want 6", got)
	}
}

func TestValidateRejectsGrantShorterThanPoll(t *testing.T) {
	cfg := Config{
		AuthMode: "development", HMACKey: make([]byte, 32), InstallationID: "i", HMACKeyID: "k",
		SlotWidth: time.Second, ReadyWindow: time.Second, PageBits: 8,
		PollInterval: 3 * time.Second, GrantTTL: 3 * time.Second,
		BookingIdleTTL: time.Second, BookingMaxLifetime: time.Second,
		Capacity: 1, AdmissionRate: 1, AdmissionBurst: 1, MaxGrantsPerTick: 1, EventIDs: []uint64{1},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() succeeded with grant TTL no longer than polling interval")
	}
}
