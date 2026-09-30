package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pcyan3166/llmHub/hub/internal/llmhub"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	dbPath := flag.String("db", "./data/llmhub.db", "SQLite database path")
	upstream := flag.String("bifrost", "http://127.0.0.1:8081", "private Bifrost HTTP origin")
	staticDir := flag.String("ui", "../ui/llmhub/dist", "built llmHub UI directory")
	seed := flag.String("seed", "", "import configuration only when the database is empty")
	flag.Parse()
	if err := run(*listen, *dbPath, *upstream, *staticDir, *seed); err != nil {
		slog.Error("llmHub stopped", "error", err)
		os.Exit(1)
	}
}

func run(listen, dbPath, upstream, staticDir, seed string) error {
	store, err := llmhub.OpenStore(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	if seed != "" {
		current, version, err := store.Config()
		if err != nil {
			return err
		}
		if version == 1 && len(current.Projects) == 0 && len(current.Pools) == 0 {
			raw, err := os.ReadFile(seed)
			if err != nil {
				return err
			}
			var config llmhub.Config
			if err = json.Unmarshal(raw, &config); err != nil {
				return err
			}
			if _, err = store.SaveConfig(config, version); err != nil {
				return fmt.Errorf("seed configuration: %w", err)
			}
		}
	}
	service, err := llmhub.NewServer(store, upstream, os.Getenv("LLMHUB_ADMIN_TOKEN"), staticDir)
	if err != nil {
		return err
	}
	defer service.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: listen, Handler: service, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	errors := make(chan error, 1)
	go func() { errors <- server.ListenAndServe() }()
	maintenance := time.NewTicker(time.Minute)
	defer maintenance.Stop()
	slog.Info("llmHub listening", "address", listen, "bifrost", upstream, "sqlite", dbPath)
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				_ = server.Close()
				return err
			}
			return nil
		case err := <-errors:
			if err == http.ErrServerClosed {
				return nil
			}
			return err
		case <-maintenance.C:
			if err := store.PruneRates(); err != nil {
				slog.Error("rate history cleanup failed", "error", err)
			}
		}
	}
}
