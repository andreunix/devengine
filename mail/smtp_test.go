package mail

import (
	"context"
	"crypto/tls"
	"strings"
	"testing"
	"time"
)

func TestNewSMTPValidatesTransportConfiguration(t *testing.T) {
	tests := []SMTPConfig{
		{},
		{Host: "smtp.example.test\r\nX", Port: 25},
		{Host: "smtp.example.test", Port: 0},
		{Host: "smtp.example.test", Port: 25, Username: "user"},
		{Host: "smtp.example.test", Port: 25, StartTLS: true, ImplicitTLS: true},
		{Host: "smtp.example.test", Port: 25, Timeout: -time.Second},
		{Host: "smtp.example.test", Port: 25, TLSConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	for _, config := range tests {
		if _, err := NewSMTP(config); err == nil {
			t.Fatalf("NewSMTP(%+v) accepted invalid configuration", config)
		}
	}
	transport, err := NewSMTP(SMTPConfig{Host: " smtp.example.test ", Port: 587, StartTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	if transport.config.Host != "smtp.example.test" || transport.config.Timeout != 10*time.Second {
		t.Fatalf("normalized config=%+v", transport.config)
	}
}

func TestMessageValidationRejectsHeaderInjectionBeforeDial(t *testing.T) {
	transport, err := NewSMTP(SMTPConfig{Host: "smtp.invalid", Port: 25})
	if err != nil {
		t.Fatal(err)
	}
	base := Message{From: Address{Address: "from@example.test"}, To: Address{Address: "to@example.test"}, Subject: "Subject", Body: "secret body"}
	for _, mutate := range []func(*Message){
		func(message *Message) { message.From.Address += "\r\nBcc: attacker@example.test" },
		func(message *Message) { message.To.Name = "Recipient\nBcc" },
		func(message *Message) { message.Subject += "\r\nBcc: attacker@example.test" },
	} {
		message := base
		mutate(&message)
		err := transport.Send(context.Background(), message)
		if err == nil || strings.Contains(err.Error(), base.Body) {
			t.Fatalf("Send() error=%v", err)
		}
	}
}

func TestRenderPreservesCompatiblePlainTextFormat(t *testing.T) {
	from, _ := validateAddress(Address{Name: "Tecno ID", Address: "noreply@example.test"})
	to, _ := validateAddress(Address{Name: "Usuário", Address: "user@example.test"})
	raw := string(render(from, to, "Confirme seu e-mail", "body\n\nhttps://example.test/action"))
	for _, required := range []string{"From: \"Tecno ID\" <noreply@example.test>\r\n", "To: =?utf-8?", "Subject: Confirme seu e-mail\r\n", "Content-Type: text/plain; charset=UTF-8\r\n", "\r\n\r\nbody\n\nhttps://example.test/action"} {
		if !strings.Contains(raw, required) {
			t.Fatalf("rendered message missing %q:\n%s", required, raw)
		}
	}
}
