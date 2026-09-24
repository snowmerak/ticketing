package config

import (
	"math"
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
		AuthMode: "development", InstallationID: "i", TicketKeyRedisAddr: "127.0.0.1:16380",
		TicketTTL: time.Minute, TicketKeyLifetime: time.Hour,
		SlotWidth: time.Second, ReadyWindow: time.Second, PageBits: 8,
		PollInterval: 3 * time.Second, GrantTTL: 3 * time.Second,
		BookingIdleTTL: time.Second, BookingMaxLifetime: time.Second,
		DependencyTimeout: time.Second,
		Capacity:          1, AdmissionRate: 1, AdmissionBurst: 1, MaxGrantsPerTick: 1, EventIDs: []uint64{1},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() succeeded with grant TTL no longer than polling interval")
	}
}

func TestValidateRejectsInvalidTicketKeyRetention(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, ttl := range []time.Duration{0, -time.Second, time.Duration(math.MaxInt64)} {
		candidate := cfg
		candidate.TicketTTL = ttl
		if err := candidate.Validate(); err == nil {
			t.Fatalf("Validate() accepted ticket TTL %v", ttl)
		}
	}
	cfg.TicketKeyLifetime = 25 * time.Hour
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a key lifetime over 24 hours")
	}
}

func TestValidateRejectsNonpositiveDependencyTimeout(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DependencyTimeout = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a zero dependency timeout")
	}
}
