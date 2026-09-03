// Package mail provides a domain-neutral SMTP transport. Products retain
// ownership of templates, subjects, links, and notification semantics.
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	stdmail "net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTPConfig describes transport concerns only. StartTLS upgrades a plain
// connection; ImplicitTLS starts TLS before the SMTP handshake.
type SMTPConfig struct {
	Host        string
	Port        int
	Username    string
	Password    string
	StartTLS    bool
	ImplicitTLS bool
	TLSConfig   *tls.Config
	Timeout     time.Duration
}

// Address is an RFC 5322 mailbox without transport or product semantics.
type Address struct {
	Name    string
	Address string
}

// Message is a UTF-8 plain-text email.
type Message struct {
	From    Address
	To      Address
	Subject string
	Body    string
}

// SMTP transports messages through one configured SMTP endpoint.
type SMTP struct{ config SMTPConfig }

// ValidateAddress rejects malformed mailboxes and header injection.
func ValidateAddress(value Address) error {
	_, err := validateAddress(value)
	return err
}

// NewSMTP validates and copies transport configuration.
func NewSMTP(config SMTPConfig) (*SMTP, error) {
	config.Host = strings.TrimSpace(config.Host)
	if config.Host == "" || strings.ContainsAny(config.Host, "\r\n") || strings.Contains(config.Host, "://") || config.Port < 1 || config.Port > 65535 {
		return nil, errors.New("mail: invalid SMTP host or port")
	}
	if config.StartTLS && config.ImplicitTLS {
		return nil, errors.New("mail: StartTLS and implicit TLS are mutually exclusive")
	}
	if (config.Username == "") != (config.Password == "") {
		return nil, errors.New("mail: SMTP username and password must be configured together")
	}
	if config.Timeout < 0 {
		return nil, errors.New("mail: invalid SMTP timeout")
	}
	if config.Timeout == 0 {
		config.Timeout = 10 * time.Second
	}
	if config.TLSConfig != nil {
		if config.TLSConfig.InsecureSkipVerify {
			return nil, errors.New("mail: TLS certificate verification cannot be disabled")
		}
		if config.TLSConfig.MinVersion != 0 && config.TLSConfig.MinVersion < tls.VersionTLS12 {
			return nil, errors.New("mail: TLS versions below 1.2 are unsupported")
		}
		config.TLSConfig = config.TLSConfig.Clone()
	}
	return &SMTP{config: config}, nil
}

// Send validates, renders, and delivers message. Errors never include the
// message body or credentials.
func (transport *SMTP) Send(ctx context.Context, message Message) error {
	if transport == nil {
		return errors.New("mail: SMTP transport is not configured")
	}
	from, err := validateAddress(message.From)
	if err != nil {
		return fmt.Errorf("mail: invalid sender: %w", err)
	}
	to, err := validateAddress(message.To)
	if err != nil {
		return fmt.Errorf("mail: invalid recipient: %w", err)
	}
	if strings.TrimSpace(message.Subject) == "" || strings.ContainsAny(message.Subject, "\r\n") {
		return errors.New("mail: invalid subject")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("mail: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, transport.config.Timeout)
	defer cancel()
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(transport.config.Host, strconv.Itoa(transport.config.Port)))
	if err != nil {
		return transportError(ctx, "connect", err)
	}
	defer connection.Close()
	stopClose := closeWhenDone(ctx, connection)
	defer stopClose()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return fmt.Errorf("mail: set SMTP deadline: %w", err)
		}
	}

	if transport.config.ImplicitTLS {
		secure := tls.Client(connection, transport.tlsConfig())
		if err := secure.HandshakeContext(ctx); err != nil {
			return transportError(ctx, "TLS handshake", err)
		}
		connection = secure
	}
	client, err := smtp.NewClient(connection, transport.config.Host)
	if err != nil {
		return transportError(ctx, "initialize SMTP client", err)
	}
	defer client.Close()
	if transport.config.StartTLS {
		if supported, _ := client.Extension("STARTTLS"); !supported {
			return errors.New("mail: SMTP server does not advertise STARTTLS")
		}
		if err := client.StartTLS(transport.tlsConfig()); err != nil {
			return transportError(ctx, "start TLS", err)
		}
	}
	if transport.config.Username != "" {
		auth := smtp.PlainAuth("", transport.config.Username, transport.config.Password, transport.config.Host)
		if err := client.Auth(auth); err != nil {
			return transportError(ctx, "authenticate", err)
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return transportError(ctx, "set sender", err)
	}
	if err := client.Rcpt(to.Address); err != nil {
		return transportError(ctx, "set recipient", err)
	}
	writer, err := client.Data()
	if err != nil {
		return transportError(ctx, "start message data", err)
	}
	raw := render(from, to, message.Subject, message.Body)
	if _, err := writer.Write(raw); err != nil {
		_ = writer.Close()
		return transportError(ctx, "write message data", err)
	}
	if err := writer.Close(); err != nil {
		return transportError(ctx, "finish message data", err)
	}
	if err := client.Quit(); err != nil {
		return transportError(ctx, "quit", err)
	}
	return nil
}

func (transport *SMTP) tlsConfig() *tls.Config {
	config := transport.config.TLSConfig
	if config == nil {
		config = &tls.Config{}
	} else {
		config = config.Clone()
	}
	if config.ServerName == "" {
		config.ServerName = transport.config.Host
	}
	if config.MinVersion == 0 {
		config.MinVersion = tls.VersionTLS12
	}
	return config
}

func validateAddress(value Address) (*stdmail.Address, error) {
	if strings.TrimSpace(value.Name) != value.Name || strings.TrimSpace(value.Address) != value.Address || strings.ContainsAny(value.Name+value.Address, "\r\n") {
		return nil, errors.New("invalid email address")
	}
	parsed, err := stdmail.ParseAddress(value.Address)
	if err != nil || parsed.Address != value.Address {
		return nil, errors.New("invalid email address")
	}
	return &stdmail.Address{Name: value.Name, Address: value.Address}, nil
}

func render(from, to *stdmail.Address, subject, body string) []byte {
	return []byte("From: " + from.String() + "\r\nTo: " + to.String() + "\r\nSubject: " + mime.QEncoding.Encode("UTF-8", subject) + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body)
}

func closeWhenDone(ctx context.Context, connection net.Conn) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}

func transportError(ctx context.Context, operation string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("mail: SMTP %s: %w", operation, ctxErr)
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return fmt.Errorf("mail: SMTP %s: %w", operation, context.DeadlineExceeded)
	}
	return fmt.Errorf("mail: SMTP %s failed: %w", operation, err)
}
