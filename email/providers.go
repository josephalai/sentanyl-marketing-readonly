package email

// EmailProvider defines the interface for sending emails through various providers.
type EmailProvider interface {
	SendEmail(from, to, subject, htmlBody, replyTo string) error
	Name() string
}

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

// BrevoProvider handles email sending via Brevo (Sendinblue).
type BrevoProvider struct {
	APIKey string
}

func NewBrevoProvider(apiKey string) *BrevoProvider {
	return &BrevoProvider{APIKey: apiKey}
}

func (b *BrevoProvider) SendEmail(from, to, subject, htmlBody, replyTo string) error {
	// TODO: Implement Brevo sending via Brevo API v3
	return nil
}

func (b *BrevoProvider) Name() string {
	return "brevo"
}

// PowerMTAProvider handles email sending via PowerMTA SMTP injection.
type PowerMTAProvider struct {
	Host     string
	Port     int
	Username string
	Password string
}

func NewPowerMTAProvider(host string, port int, username, password string) *PowerMTAProvider {
	return &PowerMTAProvider{Host: host, Port: port, Username: username, Password: password}
}

func (p *PowerMTAProvider) SendEmail(from, to, subject, htmlBody, replyTo string) error {
	// TODO: Implement PowerMTA SMTP injection with virtual MTA support
	return nil
}

func (p *PowerMTAProvider) Name() string {
	return "powermta"
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

// SendEmail dispatches an email through the configured provider.
// TODO: Implement provider selection based on config (mailhog, powermta, brevo, mailgun).
func SendEmail(from, to, subject, htmlBody, replyTo, vmta string, provider EmailProvider) error {
	if provider == nil {
		// TODO: Select default provider from config
		return nil
	}
	return provider.SendEmail(from, to, subject, htmlBody, replyTo)
}
