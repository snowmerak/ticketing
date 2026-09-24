// benchfixture owns a synthetic event for bounded local k6 runs.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"log"
	"math/big"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/snowmerak/ticketing/internal/config"
	"github.com/snowmerak/ticketing/internal/database"
)

const (
	minEventID  = uint64(3_600_000_000)
	maxEventID  = uint64(3_700_000_000)
	markerValue = "ticketing-k6-fixture-v1"
	seatCount   = 256
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if len(os.Args) < 2 || (os.Args[1] != "prepare" && os.Args[1] != "cleanup") {
		return fmt.Errorf("usage: benchfixture prepare | cleanup <event-id>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(cfg.MySQLDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return err
	}
	if os.Args[1] == "prepare" {
		id, err := unusedEventID(ctx, db, rdb)
		if err != nil {
			return err
		}
		if err := rdb.Set(ctx, marker(id), markerValue, 0).Err(); err != nil {
			return err
		}
		if err := seed(ctx, db, id); err != nil {
			return fmt.Errorf("seed event %d (run cleanup %d): %w", id, id, err)
		}
		fmt.Println(id)
		return nil
	}
	if len(os.Args) != 3 {
		return fmt.Errorf("usage: benchfixture cleanup <event-id>")
	}
	id, err := strconv.ParseUint(os.Args[2], 10, 64)
	if err != nil || id < minEventID || id >= maxEventID {
		return fmt.Errorf("event ID is outside the benchmark fixture range")
	}
	value, err := rdb.Get(ctx, marker(id)).Result()
	if err != nil || value != markerValue {
		return fmt.Errorf("benchmark ownership marker absent for event %d", id)
	}
	return cleanup(ctx, db, rdb, id)
}

func marker(id uint64) string { return fmt.Sprintf("bench:{%d}:owner", id) }

func unusedEventID(ctx context.Context, db *sql.DB, rdb *redis.Client) (uint64, error) {
	for range 10 {
		random, err := rand.Int(rand.Reader, big.NewInt(int64(maxEventID-minEventID)))
		if err != nil {
			return 0, err
		}
		id := minEventID + uint64(random.Int64())
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM seat_definitions WHERE event_id = ?`, id).Scan(&count); err != nil {
			return 0, err
		}
		exists, err := rdb.Exists(ctx, marker(id), fmt.Sprintf("q:{%d}:control", id)).Result()
		if err != nil {
			return 0, err
		}
		if count == 0 && exists == 0 {
			return id, nil
		}
	}
	return 0, fmt.Errorf("could not find an unused benchmark event ID")
}

func seed(ctx context.Context, db *sql.DB, id uint64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	definition, err := tx.PrepareContext(ctx, `INSERT INTO seat_definitions (event_id, seat_id, section_id, display_alias, row_label, seat_number, allocation_order) VALUES (?, ?, 10, ?, 'R', ?, ?)`)
	if err != nil {
		return err
	}
	defer definition.Close()
	inventory, err := tx.PrepareContext(ctx, `INSERT INTO seat_inventory (event_id, seat_id, section_id, allocation_order) VALUES (?, ?, 10, ?)`)
	if err != nil {
		return err
	}
	defer inventory.Close()
	for seatID := 1; seatID <= seatCount; seatID++ {
		if _, err := definition.ExecContext(ctx, id, seatID, fmt.Sprintf("R%d", seatID), seatID, seatID); err != nil {
			return err
		}
		if _, err := inventory.ExecContext(ctx, id, seatID, seatID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func cleanup(ctx context.Context, db *sql.DB, rdb *redis.Client, id uint64) error {
	statements := []string{
		`DELETE FROM event_purchase_guards WHERE event_id = ?`,
		`DELETE oi FROM order_items oi JOIN orders o ON o.order_id = oi.order_id WHERE o.event_id = ?`,
		`UPDATE seat_inventory SET state = 'AVAILABLE', hold_id = NULL, order_id = NULL WHERE event_id = ?`,
		`DELETE FROM orders WHERE event_id = ?`,
		`DELETE hi FROM hold_items hi JOIN holds h ON h.hold_id = hi.hold_id WHERE h.event_id = ?`,
		`DELETE FROM holds WHERE event_id = ?`,
		`DELETE FROM seat_inventory WHERE event_id = ?`,
		`DELETE FROM seat_definitions WHERE event_id = ?`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement, id); err != nil {
			return err
		}
	}
	var cursor uint64
	for {
		keys, next, err := rdb.Scan(ctx, cursor, fmt.Sprintf("q:{%d}:*", id), 100).Result()
		if err != nil {
			return err
		}
		if len(keys) != 0 {
			if err := rdb.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return rdb.Del(ctx, marker(id)).Err()
}
