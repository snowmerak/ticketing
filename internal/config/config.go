package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr               string
	RedisAddr              string
	RedisPassword          string
	TicketKeyRedisAddr     string
	TicketKeyRedisPassword string
	MySQLDSN               string
	InstallationID         string
	AuthMode               string
	EventIDs               []uint64
	TicketKeyLifetime      time.Duration

	TicketTTL          time.Duration
	PollInterval       time.Duration
	ReadyWindow        time.Duration
	SlotWidth          time.Duration
	PageBits           uint64
	SchedulerInterval  time.Duration
	GrantTTL           time.Duration
	StatsInterval      time.Duration
	BookingIdleTTL     time.Duration
	BookingMaxLifetime time.Duration
	HoldTTL            time.Duration
	HoldReaperInterval time.Duration
	Capacity           int64
	AdmissionRate      float64
	AdmissionBurst     float64
	MaxGrantsPerTick   int
	MaxSeqPerEpoch     uint64
	DBMutationLimit    int
	DependencyTimeout  time.Duration
	ShutdownTimeout    time.Duration
	SchedulerLeaseTTL  time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:               env("HTTP_ADDR", ":8080"),
		RedisAddr:              env("REDIS_ADDR", "127.0.0.1:16379"),
		RedisPassword:          os.Getenv("REDIS_PASSWORD"),
		TicketKeyRedisAddr:     env("TICKET_KEY_REDIS_ADDR", "127.0.0.1:16380"),
		TicketKeyRedisPassword: os.Getenv("TICKET_KEY_REDIS_PASSWORD"),
		MySQLDSN:               env("MYSQL_DSN", "ticketing:ticketing@tcp(127.0.0.1:13306)/ticketing?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true"),
		InstallationID:         env("TICKETING_INSTALLATION_ID", "ticketing-local-v1"),
		AuthMode:               env("AUTH_MODE", "development"),
		TicketKeyLifetime:      duration("TICKET_KEY_LIFETIME", time.Hour),
		TicketTTL:              duration("TICKET_TTL", 20*time.Minute),
		PollInterval:           duration("POLL_INTERVAL", 3*time.Second),
		ReadyWindow:            duration("READY_WINDOW", 10*time.Second),
		SlotWidth:              duration("SLOT_WIDTH", 2*time.Second),
		PageBits:               uint64(integer("PAGE_BITS", 65536)),
		SchedulerInterval:      duration("SCHEDULER_INTERVAL", 250*time.Millisecond),
		GrantTTL:               duration("GRANT_TTL", 15*time.Second),
		StatsInterval:          duration("STATS_INTERVAL", time.Second),
		BookingIdleTTL:         duration("BOOKING_IDLE_TTL", 60*time.Second),
		BookingMaxLifetime:     duration("BOOKING_MAX_LIFETIME", 10*time.Minute),
		HoldTTL:                duration("HOLD_TTL", 120*time.Second),
		HoldReaperInterval:     duration("HOLD_REAPER_INTERVAL", time.Second),
		Capacity:               int64(integer("CAPACITY", 1000)),
		AdmissionRate:          number("ADMISSION_RATE", 100),
		AdmissionBurst:         number("ADMISSION_BURST", 25),
		MaxGrantsPerTick:       integer("MAX_GRANTS_PER_TICK", 25),
		MaxSeqPerEpoch:         uint64(integer("MAX_SEQ_PER_EPOCH", 10_000_000)),
		DBMutationLimit:        integer("DB_MUTATION_LIMIT", 64),
		DependencyTimeout:      duration("DEPENDENCY_TIMEOUT", 2*time.Second),
		ShutdownTimeout:        duration("SHUTDOWN_TIMEOUT", 10*time.Second),
		SchedulerLeaseTTL:      duration("SCHEDULER_LEASE_TTL", 2*time.Second),
	}

	events, err := parseEvents(env("EVENT_IDS", "100,200"))
	if err != nil {
		return Config{}, err
	}
	cfg.EventIDs = events

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	var errs []error
	if c.AuthMode != "development" {
		errs = append(errs, errors.New("only AUTH_MODE=development is implemented"))
	}
	if c.InstallationID == "" || c.TicketKeyRedisAddr == "" {
		errs = append(errs, errors.New("installation ID and ticket key Redis address are required"))
	}
	if c.TicketTTL <= 0 || c.TicketKeyLifetime <= 0 || c.TicketKeyLifetime > 24*time.Hour {
		errs = append(errs, errors.New("TICKET_TTL and TICKET_KEY_LIFETIME must be positive; key lifetime must not exceed 24h"))
	} else if c.TicketTTL > time.Duration(math.MaxInt64)-c.TicketKeyLifetime-time.Minute {
		errs = append(errs, errors.New("ticket key retention exceeds supported duration"))
	}
	if c.DependencyTimeout <= 0 {
		errs = append(errs, errors.New("DEPENDENCY_TIMEOUT must be positive"))
	}
	if c.SlotWidth <= 0 || c.ReadyWindow < c.SlotWidth {
		errs = append(errs, errors.New("READY_WINDOW must be at least SLOT_WIDTH and both must be positive"))
	}
	if c.PageBits == 0 || c.PageBits%8 != 0 {
		errs = append(errs, errors.New("PAGE_BITS must be a positive multiple of 8"))
	}
	if c.GrantTTL <= c.PollInterval {
		errs = append(errs, errors.New("GRANT_TTL must exceed POLL_INTERVAL"))
	}
	if c.BookingIdleTTL <= 0 || c.BookingMaxLifetime < c.BookingIdleTTL {
		errs = append(errs, errors.New("BOOKING_MAX_LIFETIME must be at least BOOKING_IDLE_TTL"))
	}
	if c.Capacity <= 0 || c.AdmissionRate <= 0 || c.AdmissionBurst <= 0 || c.MaxGrantsPerTick <= 0 {
		errs = append(errs, errors.New("admission limits must be positive"))
	}
	if len(c.EventIDs) == 0 {
		errs = append(errs, errors.New("at least one EVENT_IDS value is required"))
	}
	return errors.Join(errs...)
}

func (c Config) ReadySlots() int64 {
	return int64((c.ReadyWindow+c.SlotWidth-1)/c.SlotWidth) + 1
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func duration(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return -1
	}
	return parsed
}

func integer(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return parsed
}

func number(name string, fallback float64) float64 {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return -1
	}
	return parsed
}

func parseEvents(value string) ([]uint64, error) {
	parts := strings.Split(value, ",")
	result := make([]uint64, 0, len(parts))
	seen := make(map[uint64]struct{}, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseUint(strings.TrimSpace(part), 10, 64)
		if err != nil || id == 0 {
			return nil, fmt.Errorf("invalid EVENT_IDS value %q", part)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}
