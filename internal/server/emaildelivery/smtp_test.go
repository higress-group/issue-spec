package emaildelivery

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDeterministicMessageIDAndPlainTextNormalization(t *testing.T) {
	id := uuid.MustParse("b4a33ed5-5abe-4e98-87ac-1e2703212d4f")
	first, err := DeterministicMessageID(id, "notices@example.test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := DeterministicMessageID(id, "notices@example.test")
	if err != nil || second != first || first != "<issue-spec.delivery.b4a33ed5-5abe-4e98-87ac-1e2703212d4f@example.test>" {
		t.Fatalf("message ids = %q, %q, %v", first, second, err)
	}
	to, subject, body, err := normalizeMessage(Message{DeliveryID: id, To: "person@example.test",
		Subject: "hello\r\nBcc: hidden@example.test", Body: "line one\x00\r\nline two", OccurredAt: time.Now()})
	if err != nil || to != "person@example.test" || strings.ContainsAny(subject, "\r\n") ||
		subject != "helloBcc: hidden@example.test" || body != "line one\nline two" {
		t.Fatalf("normalized = %q, %q, %q, %v", to, subject, body, err)
	}
}

func TestMailTypesDoNotFormatPrivateContent(t *testing.T) {
	message := Message{DeliveryID: uuid.New(), To: "person@example.test", Subject: "private subject", Body: "private body"}
	sender, err := NewImplicitTLSSender(SMTPConfig{Host: "mail.example.test", Port: 2465,
		Username: "mailer@example.test", Password: "example-credential", FromAddress: "notices@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{fmt.Sprintf("%v", message), fmt.Sprintf("%+v", message),
		fmt.Sprintf("%v", sender), fmt.Sprintf("%+v", sender)} {
		if strings.Contains(value, "person@example.test") || strings.Contains(value, "private") ||
			strings.Contains(value, "example-credential") || strings.Contains(value, "mailer@example.test") {
			t.Fatalf("formatted mail value leaked content: %q", value)
		}
	}
}

func TestImplicitTLSSenderAuthenticatesAfterTLSAndAcceptsData(t *testing.T) {
	fixture := newSMTPFixture(t, smtpBehavior{})
	sender := fixture.sender(t)
	id := uuid.MustParse("b4a33ed5-5abe-4e98-87ac-1e2703212d4f")
	err := sender.Send(t.Context(), Message{DeliveryID: id, To: "person@example.test", Subject: "Build notice",
		Body: "plain body", OccurredAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	result := fixture.wait(t)
	if !result.tls || len(result.commands) < 5 || !strings.HasPrefix(result.commands[0], "EHLO ") ||
		!strings.HasPrefix(result.commands[1], "AUTH PLAIN ") || !strings.HasPrefix(result.commands[2], "MAIL FROM:") ||
		!strings.HasPrefix(result.commands[3], "RCPT TO:") || result.commands[4] != "DATA" {
		t.Fatalf("SMTP ordering = tls:%v commands:%v", result.tls, result.commands)
	}
	if !strings.Contains(result.data, "Message-ID: <issue-spec.delivery."+id.String()+"@example.test>") ||
		!strings.Contains(result.data, "Content-Type: text/plain; charset=UTF-8") || strings.Contains(result.data, "text/html") {
		t.Fatalf("message payload = %q", result.data)
	}
}

func TestImplicitTLSSenderClassifiesRelayOutcomesWithoutRelayText(t *testing.T) {
	tests := []struct {
		name      string
		behavior  smtpBehavior
		reason    ReasonCode
		retryable bool
	}{
		{name: "temporary recipient rejection", behavior: smtpBehavior{rcptCode: 450}, reason: ReasonSMTPRejected, retryable: true},
		{name: "terminal recipient rejection", behavior: smtpBehavior{rcptCode: 550}, reason: ReasonSMTPRejected},
		{name: "ambiguous acceptance", behavior: smtpBehavior{ambiguousData: true}, reason: ReasonSMTPAmbiguous, retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSMTPFixture(t, test.behavior)
			err := fixture.sender(t).Send(t.Context(), Message{DeliveryID: uuid.New(), To: "person@example.test",
				Subject: "Notice", Body: "body", OccurredAt: time.Now()})
			var outcome *OutcomeError
			if !errors.As(err, &outcome) || outcome.Reason != test.reason || outcome.Retryable != test.retryable {
				t.Fatalf("error = %#v", err)
			}
			if strings.Contains(err.Error(), "fixture rejection detail") {
				t.Fatalf("relay text leaked: %v", err)
			}
			_ = fixture.wait(t)
		})
	}
}

func TestImplicitTLSSenderClassifiesAuthenticationOutcomes(t *testing.T) {
	tests := []struct {
		name      string
		behavior  smtpBehavior
		timeout   time.Duration
		reason    ReasonCode
		retryable bool
	}{
		{name: "temporary authentication rejection", behavior: smtpBehavior{authCode: 454}, reason: ReasonSMTPAuthentication, retryable: true},
		{name: "terminal credential rejection", behavior: smtpBehavior{authCode: 535}, reason: ReasonSMTPAuthentication},
		{name: "authentication timeout", behavior: smtpBehavior{authDelay: 250 * time.Millisecond}, timeout: 50 * time.Millisecond, reason: ReasonSMTPTimeout, retryable: true},
		{name: "authentication connection loss", behavior: smtpBehavior{authClose: true}, reason: ReasonSMTPUnavailable, retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSMTPFixture(t, test.behavior)
			timeout := test.timeout
			if timeout == 0 {
				timeout = 2 * time.Second
			}
			err := fixture.senderWithTimeout(t, timeout).Send(t.Context(), Message{DeliveryID: uuid.New(), To: "person@example.test",
				Subject: "Notice", Body: "body", OccurredAt: time.Now()})
			var outcome *OutcomeError
			if !errors.As(err, &outcome) || outcome.Reason != test.reason || outcome.Retryable != test.retryable {
				t.Fatalf("error = %#v", err)
			}
			if strings.Contains(err.Error(), "fixture authentication detail") {
				t.Fatalf("relay text leaked: %v", err)
			}
			_ = fixture.wait(t)
		})
	}
}

func TestImplicitTLSSenderValidatesHostnameAndRefusesPlaintext(t *testing.T) {
	fixture := newSMTPFixture(t, smtpBehavior{})
	sender, err := NewImplicitTLSSender(SMTPConfig{Host: "localhost", Port: fixture.port,
		Username: "mailer@example.test", Password: "example-credential", FromAddress: "notices@example.test",
		RootCAs: x509.NewCertPool(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(t.Context(), Message{DeliveryID: uuid.New(), To: "person@example.test",
		Subject: "Notice", Body: "body", OccurredAt: time.Now()})
	var outcome *OutcomeError
	if !errors.As(err, &outcome) || outcome.Reason != ReasonSMTPTLSValidation || outcome.Retryable {
		t.Fatalf("certificate error = %#v", err)
	}
	fixture.allowFailure = true
	_ = fixture.wait(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	firstBytes := make(chan string, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			firstBytes <- ""
			return
		}
		defer connection.Close()
		_ = connection.SetReadDeadline(time.Now().Add(time.Second))
		buffer := make([]byte, 8)
		count, _ := connection.Read(buffer)
		firstBytes <- string(buffer[:count])
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	plainSender, err := NewImplicitTLSSender(SMTPConfig{Host: "localhost", Port: port,
		Username: "mailer@example.test", Password: "example-credential", FromAddress: "notices@example.test",
		RootCAs: fixture.roots, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_ = plainSender.Send(t.Context(), Message{DeliveryID: uuid.New(), To: "person@example.test",
		Subject: "Notice", Body: "body", OccurredAt: time.Now()})
	observed := <-firstBytes
	if strings.HasPrefix(observed, "EHLO") || strings.HasPrefix(observed, "HELO") || strings.HasPrefix(observed, "AUTH") {
		t.Fatalf("sender began plaintext SMTP: %q", observed)
	}
}

type smtpBehavior struct {
	rcptCode      int
	ambiguousData bool
	authCode      int
	authClose     bool
	authDelay     time.Duration
}

type smtpResult struct {
	tls      bool
	commands []string
	data     string
	err      error
}

type smtpFixture struct {
	listener     net.Listener
	port         int
	roots        *x509.CertPool
	done         chan smtpResult
	once         sync.Once
	allowFailure bool
}

func newSMTPFixture(t *testing.T, behavior smtpBehavior) *smtpFixture {
	t.Helper()
	certificate, roots := testCertificate(t)
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := tls.NewListener(tcpListener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	fixture := &smtpFixture{listener: listener, port: tcpListener.Addr().(*net.TCPAddr).Port,
		roots: roots, done: make(chan smtpResult, 1)}
	go fixture.serve(behavior)
	t.Cleanup(func() { fixture.close() })
	return fixture
}

func (f *smtpFixture) sender(t *testing.T) *ImplicitTLSSender {
	return f.senderWithTimeout(t, 2*time.Second)
}

func (f *smtpFixture) senderWithTimeout(t *testing.T, timeout time.Duration) *ImplicitTLSSender {
	t.Helper()
	sender, err := NewImplicitTLSSender(SMTPConfig{Host: "localhost", Port: f.port,
		Username: "mailer@example.test", Password: "example-credential", FromAddress: "notices@example.test",
		RootCAs: f.roots, Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	return sender
}

func (f *smtpFixture) serve(behavior smtpBehavior) {
	result := smtpResult{}
	connection, err := f.listener.Accept()
	if err != nil {
		result.err = err
		f.done <- result
		return
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	if tlsConnection, ok := connection.(*tls.Conn); ok {
		if err := tlsConnection.Handshake(); err != nil {
			result.err = err
			f.done <- result
			return
		}
		result.tls = tlsConnection.ConnectionState().HandshakeComplete
	}
	protocol := textproto.NewConn(connection)
	defer protocol.Close()
	if err := protocol.PrintfLine("220 fixture.example.test ESMTP"); err != nil {
		result.err = err
		f.done <- result
		return
	}
	read := func() (string, bool) {
		line, readErr := protocol.ReadLine()
		if readErr != nil {
			result.err = readErr
			return "", false
		}
		result.commands = append(result.commands, line)
		return line, true
	}
	if _, ok := read(); !ok {
		f.done <- result
		return
	}
	_ = protocol.PrintfLine("250-fixture.example.test")
	_ = protocol.PrintfLine("250 AUTH PLAIN LOGIN")
	if _, ok := read(); !ok {
		f.done <- result
		return
	}
	if behavior.authClose {
		f.done <- result
		return
	}
	if behavior.authDelay > 0 {
		time.Sleep(behavior.authDelay)
		f.done <- result
		return
	}
	if behavior.authCode != 0 {
		_ = protocol.PrintfLine("%d fixture authentication detail", behavior.authCode)
		f.done <- result
		return
	}
	_ = protocol.PrintfLine("235 2.7.0 accepted")
	if _, ok := read(); !ok {
		f.done <- result
		return
	}
	_ = protocol.PrintfLine("250 2.1.0 accepted")
	if _, ok := read(); !ok {
		f.done <- result
		return
	}
	if behavior.rcptCode != 0 {
		_ = protocol.PrintfLine("%d fixture rejection detail", behavior.rcptCode)
		f.done <- result
		return
	}
	_ = protocol.PrintfLine("250 2.1.5 accepted")
	if _, ok := read(); !ok {
		f.done <- result
		return
	}
	_ = protocol.PrintfLine("354 send data")
	payload, readErr := io.ReadAll(protocol.DotReader())
	result.data = string(payload)
	if readErr != nil {
		result.err = readErr
		f.done <- result
		return
	}
	if behavior.ambiguousData {
		f.done <- result
		return
	}
	_ = protocol.PrintfLine("250 2.0.0 queued")
	if line, ok := read(); ok && strings.HasPrefix(line, "QUIT") {
		_ = protocol.PrintfLine("221 2.0.0 bye")
	}
	f.done <- result
}

func (f *smtpFixture) wait(t *testing.T) smtpResult {
	t.Helper()
	select {
	case result := <-f.done:
		if result.err != nil && !f.allowFailure {
			t.Fatalf("SMTP fixture: %v", result.err)
		}
		return result
	case <-time.After(4 * time.Second):
		t.Fatal("SMTP fixture timed out")
		return smtpResult{}
	}
}

func (f *smtpFixture) close() { f.once.Do(func() { _ = f.listener.Close() }) }

func testCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), DNSNames: []string{"localhost"},
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(t, private)}))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return certificate, roots
}

func mustPKCS8(t *testing.T, key any) []byte {
	t.Helper()
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
