// Package server owns the isolated HTTP lifecycle for self-hosted runner
// webhook intake. It intentionally has no GitHub notification dependencies.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	webhook "github.com/higress-group/issue-spec/internal/commentrunner/intake/webhook"
)

type Config struct {
	ListenAddress     string
	TLSCertFile       string
	TLSKeyFile        string
	Production        bool
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxHeaderBytes    int
	MaxConnections    int
	Listener          net.Listener
}

type Service struct {
	config      Config
	handler     *webhook.Handler
	certificate []tls.Certificate
}

func (s *Service) StopAccepting() {
	if s != nil && s.handler != nil {
		s.handler.StopAccepting()
	}
}

func New(config Config, handler *webhook.Handler) (*Service, error) {
	if handler == nil {
		return nil, errors.New("runner webhook server: handler is required")
	}
	if config.Listener == nil && strings.TrimSpace(config.ListenAddress) == "" {
		config.ListenAddress = "127.0.0.1:9876"
	}
	if (config.TLSCertFile == "") != (config.TLSKeyFile == "") {
		return nil, errors.New("runner webhook server: TLS certificate and key must be configured together")
	}
	bindAddress := config.ListenAddress
	if config.Listener != nil {
		bindAddress = config.Listener.Addr().String()
	}
	if err := validateBind(bindAddress, config.TLSCertFile != "", config.Production); err != nil {
		return nil, err
	}
	if config.Production && config.TLSCertFile == "" {
		return nil, errors.New("runner webhook server: production mode requires TLS")
	}
	var certificates []tls.Certificate
	if config.TLSCertFile != "" {
		certificatePEM, err := readLimitedFile(config.TLSCertFile, 1<<20)
		if err != nil {
			return nil, fmt.Errorf("runner webhook server: read TLS certificate: %w", err)
		}
		keyPEM, err := (SecretReference{File: config.TLSKeyFile}).Load()
		if err != nil {
			return nil, fmt.Errorf("runner webhook server: read TLS key: %w", err)
		}
		certificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
		clear(keyPEM)
		if err != nil {
			return nil, fmt.Errorf("runner webhook server: parse TLS key pair: %w", err)
		}
		certificates = []tls.Certificate{certificate}
	}
	if config.ReadHeaderTimeout <= 0 {
		config.ReadHeaderTimeout = 5 * time.Second
	}
	if config.ReadTimeout <= 0 {
		config.ReadTimeout = 15 * time.Second
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = 15 * time.Second
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = 60 * time.Second
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = 30 * time.Second
	}
	if config.MaxHeaderBytes <= 0 {
		config.MaxHeaderBytes = 32 << 10
	}
	if config.MaxConnections <= 0 {
		config.MaxConnections = 128
	}
	if config.ReadHeaderTimeout > time.Minute || config.ReadTimeout > 5*time.Minute ||
		config.WriteTimeout > 5*time.Minute || config.IdleTimeout > 10*time.Minute ||
		config.ShutdownTimeout > 5*time.Minute || config.MaxHeaderBytes > 1<<20 || config.MaxConnections > 4096 {
		return nil, errors.New("runner webhook server: limits exceed safe bounds")
	}
	return &Service{config: config, handler: handler, certificate: certificates}, nil
}

func readLimitedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	value, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(value)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return value, nil
}

func (s *Service) Run(ctx context.Context) error {
	listener := s.config.Listener
	var err error
	if listener == nil {
		listener, err = net.Listen("tcp", s.config.ListenAddress)
		if err != nil {
			return fmt.Errorf("runner webhook server: listen: %w", err)
		}
	}
	limited := newLimitListener(listener, s.config.MaxConnections)
	httpServer := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: s.config.ReadHeaderTimeout,
		ReadTimeout:       s.config.ReadTimeout,
		WriteTimeout:      s.config.WriteTimeout,
		IdleTimeout:       s.config.IdleTimeout,
		MaxHeaderBytes:    s.config.MaxHeaderBytes,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, Certificates: s.certificate},
	}
	serveErr := make(chan error, 1)
	go func() {
		if s.config.TLSCertFile != "" {
			serveErr <- httpServer.ServeTLS(limited, "", "")
			return
		}
		serveErr <- httpServer.Serve(limited)
	}()
	select {
	case err := <-serveErr:
		s.StopAccepting()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("runner webhook server: serve: %w", err)
	case <-ctx.Done():
		s.StopAccepting()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.config.ShutdownTimeout)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			_ = httpServer.Close()
			return fmt.Errorf("runner webhook server: shutdown: %w", err)
		}
		err := <-serveErr
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("runner webhook server: serve during shutdown: %w", err)
		}
		return nil
	}
}

func validateBind(address string, tlsEnabled, production bool) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return fmt.Errorf("runner webhook server: listen address must be host:port: %w", err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return errors.New("runner webhook server: wildcard listen addresses are not allowed")
	}
	loopback := strings.EqualFold(host, "localhost")
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		loopback = ip.IsLoopback()
	}
	if !tlsEnabled && !loopback {
		return errors.New("runner webhook server: plaintext listeners are restricted to loopback")
	}
	if production && loopback {
		return errors.New("runner webhook server: production listener must use an explicit non-loopback address")
	}
	return nil
}

type limitListener struct {
	net.Listener
	sem chan struct{}
}

func newLimitListener(listener net.Listener, limit int) *limitListener {
	return &limitListener{Listener: listener, sem: make(chan struct{}, limit)}
}

func (l *limitListener) Accept() (net.Conn, error) {
	l.sem <- struct{}{}
	connection, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitedConn{Conn: connection, release: func() { <-l.sem }}, nil
}

type limitedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
