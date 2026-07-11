package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/higress-group/issue-spec/internal/server/config"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, "issue-spec-server healthcheck:", err)
			os.Exit(1)
		}
		return
	}
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

func healthcheck() error {
	address := os.Getenv("ISSUE_SPEC_HEALTHCHECK_URL")
	if address == "" {
		address = "http://127.0.0.1:8080/readyz"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(address)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New(response.Status)
	}
	return nil
}
