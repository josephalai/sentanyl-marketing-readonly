package email

import (
	"sync"

	"github.com/josephalai/sentanyl/pkg/emailer"
)

// EmailProvider defines the interface for sending emails through various providers.
type EmailProvider interface {
	SendEmail(from, to, subject, htmlBody, replyTo string) error
	Name() string
}

// DefaultProvider returns the process-wide provider selected by
// EMAIL_PROVIDER (mailhog | smtp | powermta | brevo | warmup).
// "warmup" routes through PowerMTA within each domain's daily warmup cap
// and overflows/falls back to Brevo.
func DefaultProvider() EmailProvider {
	defaultOnce.Do(func() { defaultProvider = emailer.FromEnv() })
	return defaultProvider
}

var (
	defaultOnce     sync.Once
	defaultProvider EmailProvider
)

// MailgunProvider handles email sending via Mailgun.
type MailgunProvider struct {
	Domain string
	APIKey string
}

func NewMailgunProvider(domain, apiKey string) *MailgunProvider {
	return &MailgunProvider{Domain: domain, APIKey: apiKey}
}

func (m *MailgunProvider) SendEmail(from, to, subject, htmlBody, replyTo string) error {
	// TODO: Implement Mailgun sending via github.com/mailgun/mailgun-go
	return nil
}

func (m *MailgunProvider) Name() string {
	return "mailgun"
}

// BrevoProvider handles email sending via the Brevo (Sendinblue) API v3.
type BrevoProvider struct {
	brevo *emailer.Brevo
}

func NewBrevoProvider(apiKey string) *BrevoProvider {
	return &BrevoProvider{brevo: &emailer.Brevo{APIKey: apiKey}}
}

func (b *BrevoProvider) SendEmail(from, to, subject, htmlBody, replyTo string) error {
	return b.brevo.SendEmail(from, to, subject, htmlBody, replyTo)
}

func (b *BrevoProvider) Name() string {
	return "brevo"
}

// PowerMTAProvider handles email sending via PowerMTA SMTP injection with
// per-domain VMTA routing (X-PMTA-VirtualMTA).
type PowerMTAProvider struct {
	pmta *emailer.PowerMTA
}

func NewPowerMTAProvider(host string, port int, username, password string) *PowerMTAProvider {
	return &PowerMTAProvider{pmta: emailer.NewPowerMTA(host, port, username, password)}
}

func (p *PowerMTAProvider) SendEmail(from, to, subject, htmlBody, replyTo string) error {
	return p.pmta.SendEmail(from, to, subject, htmlBody, replyTo)
}

func (p *PowerMTAProvider) Name() string {
	return "powermta"
}

// SMTPProvider sends email via plain SMTP with optional auth + STARTTLS.
// With no credentials it behaves like before (MailHog dev).
type SMTPProvider struct {
	Host string
	Port int
	smtp *emailer.SMTP
}

func NewSMTPProvider(host string, port int) *SMTPProvider {
	return NewSMTPProviderAuth(host, port, "", "")
}

func NewSMTPProviderAuth(host string, port int, username, password string) *SMTPProvider {
	return &SMTPProvider{
		Host: host,
		Port: port,
		smtp: &emailer.SMTP{Cfg: emailer.SMTPConfig{Host: host, Port: port, Username: username, Password: password}},
	}
}

func (s *SMTPProvider) SendEmail(from, to, subject, htmlBody, replyTo string) error {
	return s.smtp.SendEmail(from, to, subject, htmlBody, replyTo)
}

func (s *SMTPProvider) Name() string {
	return "smtp"
}

// TwilioProvider handles SMS sending via Twilio.
type TwilioProvider struct {
	AccountSID string
	AuthToken  string
	FromNumber string
}

func NewTwilioProvider(accountSID, authToken, fromNumber string) *TwilioProvider {
	return &TwilioProvider{AccountSID: accountSID, AuthToken: authToken, FromNumber: fromNumber}
}

func (t *TwilioProvider) SendSMS(to, body string) error {
	// TODO: Implement Twilio SMS sending
	return nil
}

func (t *TwilioProvider) Name() string {
	return "twilio"
}

// SendEmail dispatches an email through the given provider, falling back to
// the EMAIL_PROVIDER-selected default when provider is nil. vmta is legacy —
// VMTA routing now derives from the from-address domain.
func SendEmail(from, to, subject, htmlBody, replyTo, vmta string, provider EmailProvider) error {
	if provider == nil {
		provider = DefaultProvider()
	}
	return provider.SendEmail(from, to, subject, htmlBody, replyTo)
}
