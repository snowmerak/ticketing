package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/snowmerak/ticketing/internal/booking"
	"github.com/snowmerak/ticketing/internal/config"
	"github.com/snowmerak/ticketing/internal/database"
	"github.com/snowmerak/ticketing/internal/httpapi"
	"github.com/snowmerak/ticketing/internal/id"
	"github.com/snowmerak/ticketing/internal/keyredis"
	"github.com/snowmerak/ticketing/internal/queue"
	"github.com/snowmerak/ticketing/internal/ticket"
)

func main() {
	level := slog.LevelInfo
	switch os.Getenv("TICKETING_LOG_LEVEL") {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	if err := run(logger); err != nil {
		logger.Error("process_failed", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	redisClient := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, Protocol: 2})
	defer redisClient.Close()
	queueStore := queue.NewStore(redisClient, nil, cfg)

	switch command {
	case "init-state":
		if err := redisClient.Ping(ctx).Err(); err != nil {
			return err
		}
		if err := queueStore.Initialize(ctx); err != nil {
			return err
		}
		logger.Info("redis_state_initialized", "installation_id", cfg.InstallationID, "events", cfg.EventIDs)
		return nil
	case "migrate":
		db, err := database.Open(cfg.MySQLDSN)
		if err != nil {
			return err
		}
		defer db.Close()
		if err := db.PingContext(ctx); err != nil {
			return err
		}
		directory := "migrations"
		if len(os.Args) > 2 {
			directory = os.Args[2]
		}
		if absolute, err := filepath.Abs(directory); err == nil {
			directory = absolute
		}
		if err := database.Migrate(ctx, db, directory); err != nil {
			return err
		}
		logger.Info("mysql_migrations_applied", "directory", directory)
		return nil
	case "serve":
		keyClient := redis.NewClient(&redis.Options{Addr: cfg.TicketKeyRedisAddr, Password: cfg.TicketKeyRedisPassword, Protocol: 2})
		defer keyClient.Close()
		signer, err := ticket.NewSigner(keyredis.New(keyClient), cfg.TicketKeyLifetime, cfg.TicketTTL)
		if err != nil {
			return fmt.Errorf("initialize ticket signing: %w", err)
		}
		defer signer.Close()
		queueStore = queue.NewStore(redisClient, signer, cfg)
		return serve(ctx, cfg, redisClient, queueStore, signer, logger)
	default:
		return fmt.Errorf("unknown command %q; use serve, init-state, or migrate", command)
	}
}

func serve(ctx context.Context, cfg config.Config, redisClient *redis.Client, queueStore *queue.Store, signer *ticket.Signer, logger *slog.Logger) error {
	startupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := redisClient.Ping(startupCtx).Err(); err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}
	if err := queueStore.CheckInstallation(startupCtx); err != nil {
		return fmt.Errorf("check redis state (run init-state explicitly): %w", err)
	}
	db, err := database.Open(cfg.MySQLDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.PingContext(startupCtx); err != nil {
		return fmt.Errorf("connect mysql: %w", err)
	}
	bookingStore := booking.NewStore(db, cfg.HoldTTL, cfg.DBMutationLimit)
	seatCache := booking.NewSeatCache(bookingStore, cfg.EventIDs, logger)
	workerID, err := id.New()
	if err != nil {
		return err
	}
	worker := queue.NewWorker(queueStore, cfg, workerID, db.PingContext, logger)
	backgroundCtx, stopBackground := context.WithCancel(ctx)
	signerDone := make(chan struct{})
	defer func() { stopBackground(); <-signerDone }()
	go func() {
		defer close(signerDone)
		signer.Run(backgroundCtx, cfg.DependencyTimeout, func(err error) {
			if err != nil {
				logger.Warn("ticket_key_registration_retrying", "error", err)
			} else {
				logger.Info("ticket_key_registration_recovered")
			}
		})
	}()
	go worker.Run(backgroundCtx)
	go seatCache.Run(backgroundCtx)
	go runHoldReaper(backgroundCtx, bookingStore, cfg.HoldReaperInterval, logger)

	api := httpapi.New(cfg, queueStore, bookingStore, seatCache, db, logger)
	httpServer := &http.Server{
		Addr: cfg.HTTPAddr, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
	}
	serveErrors := make(chan error, 1)
	go func() {
		logger.Info("http_started", "address", cfg.HTTPAddr, "auth_mode", cfg.AuthMode)
		serveErrors <- httpServer.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		stopBackground()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func runHoldReaper(ctx context.Context, store *booking.Store, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			count, err := store.ExpireHolds(ctx, 100)
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("hold_reaper_failed", "error", err)
			} else if count > 0 {
				logger.Info("holds_expired", "count", count)
			}
		}
	}
}
