package emaildelivery

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	defaultSMTPTimeout = 15 * time.Second
	maxSubjectBytes    = 200
	maxBodyBytes       = 128 << 10
)

// SMTPConfig configures authenticated SMTP over an already established
// implicit-TLS connection. It intentionally has no plaintext or STARTTLS mode.
type SMTPConfig struct {
	Host        string
	Port        int
	Username    string
	Password    string
	FromAddress string
	Timeout     time.Duration
	RootCAs     *x509.CertPool
	Dialer      *net.Dialer
}

func (c SMTPConfig) String() string { return "<implicit TLS SMTP configured>" }

func (c SMTPConfig) GoString() string { return c.String() }

func (c SMTPConfig) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, c.String()) }

type ImplicitTLSSender struct {
	host, address, username, password, from string
	timeout                                 time.Duration
	rootCAs                                 *x509.CertPool
	dialer                                  *net.Dialer
}

func (s *ImplicitTLSSender) String() string { return "<implicit TLS SMTP sender>" }

func (s *ImplicitTLSSender) GoString() string { return s.String() }

func (s *ImplicitTLSSender) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, s.String()) }

func NewImplicitTLSSender(config SMTPConfig) (*ImplicitTLSSender, error) {
	host := strings.ToLower(strings.TrimSpace(config.Host))
	if !smtpHostValid(host) || config.Port < 1 || config.Port > 65535 {
		return nil, errors.New("email delivery: invalid SMTP network configuration")
	}
	username := strings.TrimSpace(config.Username)
	if username == "" || len(username) > 320 || strings.ContainsAny(username, "\x00\r\n ") ||
		config.Password == "" || len(config.Password) > 4096 || strings.ContainsRune(config.Password, 0) {
		return nil, errors.New("email delivery: invalid SMTP authentication configuration")
	}
	from := strings.TrimSpace(config.FromAddress)
	parsed, err := mail.ParseAddress(from)
	if err != nil || parsed.Name != "" || parsed.Address != from || strings.ContainsAny(from, "\r\n") {
		return nil, errors.New("email delivery: invalid SMTP sender configuration")
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultSMTPTimeout
	}
	dialer := config.Dialer
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	return &ImplicitTLSSender{host: host, address: net.JoinHostPort(host, strconv.Itoa(config.Port)),
		username: username, password: config.Password, from: from, timeout: timeout,
		rootCAs: config.RootCAs, dialer: dialer}, nil
}

func smtpHostValid(host string) bool {
	if host == "" || len(host) > 253 || strings.ContainsAny(host, "\x00\r\n:/[] ") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

func (s *ImplicitTLSSender) Send(ctx context.Context, message Message) error {
	to, subject, body, err := normalizeMessage(message)
	if err != nil {
		return Permanent(ReasonInvalidMessage)
	}
	messageID, err := DeterministicMessageID(message.DeliveryID, s.from)
	if err != nil {
		return Permanent(ReasonInvalidMessage)
	}
	payload, err := renderMessage(s.from, to, subject, body, messageID, message.OccurredAt)
	if err != nil {
		return Permanent(ReasonInvalidMessage)
	}

	dialer := *s.dialer
	if dialer.Timeout <= 0 || dialer.Timeout > s.timeout {
		dialer.Timeout = s.timeout
	}
	raw, err := dialer.DialContext(ctx, "tcp", s.address)
	if err != nil {
		return transportError(ctx, err)
	}
	defer raw.Close()
	deadline := time.Now().Add(s.timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := raw.SetDeadline(deadline); err != nil {
		return Retryable(ReasonSMTPUnavailable)
	}
	stopClose := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stopClose()

	tlsConnection := tls.Client(raw, &tls.Config{ServerName: s.host, RootCAs: s.rootCAs, MinVersion: tls.VersionTLS12})
	if err := tlsConnection.HandshakeContext(ctx); err != nil {
		if certificateError(err) {
			return Permanent(ReasonSMTPTLSValidation)
		}
		return transportError(ctx, err)
	}
	state := tlsConnection.ConnectionState()
	if !state.HandshakeComplete || state.Version < tls.VersionTLS12 || len(state.VerifiedChains) == 0 {
		return Permanent(ReasonSMTPTLSValidation)
	}
	client, err := smtp.NewClient(tlsConnection, s.host)
	if err != nil {
		return transportError(ctx, err)
	}
	defer client.Close()
	auth, err := selectAuth(client, s.host, s.username, s.password)
	if err != nil {
		return err
	}
	if err := client.Auth(auth); err != nil {
		return smtpAuthenticationError(ctx, err)
	}
	if err := client.Mail(s.from); err != nil {
		return smtpCommandError(err)
	}
	if err := client.Rcpt(to); err != nil {
		return smtpCommandError(err)
	}
	writer, err := client.Data()
	if err != nil {
		return smtpCommandError(err)
	}
	if _, err := writer.Write(payload); err != nil {
		_ = writer.Close()
		return transportError(ctx, err)
	}
	if err := writer.Close(); err != nil {
		var response *textproto.Error
		if errors.As(err, &response) {
			return smtpCommandError(err)
		}
		return Retryable(ReasonSMTPAmbiguous)
	}
	// A failure after the DATA command was accepted does not change relay
	// acceptance. QUIT is therefore best effort and never triggers a retry.
	_ = client.Quit()
	return nil
}

func selectAuth(client *smtp.Client, host, username, password string) (smtp.Auth, error) {
	available, value := client.Extension("AUTH")
	if !available {
		return nil, Permanent(ReasonSMTPAuthentication)
	}
	mechanisms := map[string]struct{}{}
	for _, mechanism := range strings.Fields(strings.ToUpper(value)) {
		mechanisms[mechanism] = struct{}{}
	}
	// PLAIN is preferred because net/smtp additionally verifies that the
	// connection is TLS-protected. LOGIN remains a compatibility fallback, but
	// its implementation also refuses any non-TLS server state.
	if _, ok := mechanisms["PLAIN"]; ok {
		return smtp.PlainAuth("", username, password, host), nil
	}
	if _, ok := mechanisms["LOGIN"]; ok {
		return loginAuth{username: username, password: password}, nil
	}
	return nil, Permanent(ReasonSMTPAuthentication)
}

type loginAuth struct{ username, password string }

func (a loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS {
		return "", nil, errors.New("email delivery: LOGIN requires TLS")
	}
	return "LOGIN", nil, nil
}

func (a loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	prompt := strings.ToLower(string(fromServer))
	switch {
	case strings.Contains(prompt, "username"):
		return []byte(a.username), nil
	case strings.Contains(prompt, "password"):
		return []byte(a.password), nil
	default:
		return nil, errors.New("email delivery: unexpected LOGIN challenge")
	}
}

func normalizeMessage(message Message) (string, string, string, error) {
	if message.DeliveryID == uuid.Nil || message.OccurredAt.IsZero() {
		return "", "", "", ErrInvalid
	}
	to := strings.TrimSpace(message.To)
	address, err := mail.ParseAddress(to)
	if err != nil || address.Name != "" || address.Address != to || len(to) > 320 || strings.ContainsAny(to, "\r\n") {
		return "", "", "", ErrInvalid
	}
	subject := stripControls(strings.TrimSpace(message.Subject), false)
	subject = truncateUTF8(subject, maxSubjectBytes)
	if subject == "" {
		return "", "", "", ErrInvalid
	}
	body := strings.ReplaceAll(strings.ReplaceAll(message.Body, "\r\n", "\n"), "\r", "\n")
	body = stripControls(body, true)
	body = truncateUTF8(body, maxBodyBytes)
	return to, subject, body, nil
}

func stripControls(value string, keepNewlines bool) string {
	var result strings.Builder
	result.Grow(len(value))
	for _, char := range value {
		if char == '\t' || (keepNewlines && char == '\n') || (char >= 0x20 && char != 0x7f) {
			result.WriteRune(char)
		}
	}
	return result.String()
}

func truncateUTF8(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func renderMessage(from, to, subject, body, messageID string, occurred time.Time) ([]byte, error) {
	var result bytes.Buffer
	for _, header := range []string{
		"From: " + (&mail.Address{Address: from}).String(),
		"To: " + (&mail.Address{Address: to}).String(),
		"Date: " + occurred.UTC().Format(time.RFC1123Z),
		"Message-ID: " + messageID,
		"Subject: " + mime.QEncoding.Encode("utf-8", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: quoted-printable",
		"Auto-Submitted: auto-generated",
	} {
		result.WriteString(header)
		result.WriteString("\r\n")
	}
	result.WriteString("\r\n")
	encoded := quotedprintable.NewWriter(&result)
	if _, err := encoded.Write([]byte(body)); err != nil {
		return nil, err
	}
	if err := encoded.Close(); err != nil {
		return nil, err
	}
	return result.Bytes(), nil
}

func DeterministicMessageID(deliveryID uuid.UUID, fromAddress string) (string, error) {
	if deliveryID == uuid.Nil || strings.ContainsAny(fromAddress, "\r\n") {
		return "", ErrInvalid
	}
	separator := strings.LastIndexByte(fromAddress, '@')
	if separator < 1 || separator == len(fromAddress)-1 {
		return "", ErrInvalid
	}
	domain := strings.ToLower(fromAddress[separator+1:])
	if !smtpHostValid(domain) {
		return "", ErrInvalid
	}
	return "<issue-spec.delivery." + deliveryID.String() + "@" + domain + ">", nil
}

func smtpCommandError(err error) error {
	var response *textproto.Error
	if errors.As(err, &response) && response.Code >= 400 && response.Code < 500 {
		return Retryable(ReasonSMTPRejected)
	}
	if errors.As(err, &response) {
		return Permanent(ReasonSMTPRejected)
	}
	return Retryable(ReasonSMTPUnavailable)
}

func smtpAuthenticationError(ctx context.Context, err error) error {
	var response *textproto.Error
	if errors.As(err, &response) {
		if response.Code >= 400 && response.Code < 500 {
			return Retryable(ReasonSMTPAuthentication)
		}
		return Permanent(ReasonSMTPAuthentication)
	}
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return Retryable(ReasonSMTPTimeout)
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return transportError(ctx, err)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return Retryable(ReasonSMTPUnavailable)
	}
	return Permanent(ReasonSMTPAuthentication)
}

func transportError(ctx context.Context, err error) error {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return Retryable(ReasonSMTPTimeout)
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return Retryable(ReasonSMTPTimeout)
	}
	return Retryable(ReasonSMTPUnavailable)
}

func certificateError(err error) bool {
	var verification *tls.CertificateVerificationError
	var hostname x509.HostnameError
	var authority x509.UnknownAuthorityError
	return errors.As(err, &verification) || errors.As(err, &hostname) || errors.As(err, &authority)
}
