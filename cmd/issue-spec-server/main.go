package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/higress-group/issue-spec/internal/server/config"
	"github.com/higress-group/issue-spec/internal/server/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "issue-spec-server: configuration: %v\n", config.RedactError(err))
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "issue-spec-server: %v\n", cfg.RedactError(err))
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config) error {
	database, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer database.Close()

	switch cfg.MigrationsMode {
	case config.MigrationsAuto:
		if err := database.Migrate(ctx); err != nil {
			return fmt.Errorf("migrate storage: %w", err)
		}
	case config.MigrationsValidate:
		if err := database.ValidateMigrations(ctx); err != nil {
			return fmt.Errorf("validate storage migrations: %w", err)
		}
	case config.MigrationsOff:
		// Explicit operator choice: connectivity is still checked by store.Open.
	default:
		return fmt.Errorf("unsupported migration mode %q", cfg.MigrationsMode)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		checkCtx, cancel := context.WithTimeout(request.Context(), cfg.HealthReadTimeout)
		defer cancel()
		if err := database.Ping(checkCtx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadTimeout:       cfg.HealthReadTimeout,
		ReadHeaderTimeout: cfg.HealthReadTimeout,
		WriteTimeout:      cfg.HealthWriteTimeout,
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.GracefulShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		err := <-serveErr
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve during shutdown: %w", err)
		}
		return nil
	}
}
