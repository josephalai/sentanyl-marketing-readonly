package imap

import (
	"crypto/tls"
	"fmt"
	"net/smtp"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Message is a normalized inbound email fetched from an IMAP server.
type Message struct {
	UID       uint32
	MessageID string
	InReplyTo string
	Subject   string
	FromEmail string
	FromName  string
	ToList    []string
	Date      time.Time
	BodyText  string
}

// FetchNew connects over IMAPS (TLS port 993), fetches messages with UID > lastUID,
// marks them seen, and returns them. Pass lastUID=0 to fetch recent unseen only.
func FetchNew(host string, port int, username, password string, lastUID uint32) (msgs []Message, maxUID uint32, err error) {
	maxUID = lastUID
	addr := fmt.Sprintf("%s:%d", host, port)
	tlsCfg := &tls.Config{ServerName: host}

	c, dialErr := imapclient.DialTLS(addr, &imapclient.Options{TLSConfig: tlsCfg})
	if dialErr != nil {
		c, dialErr = imapclient.DialStartTLS(addr, &imapclient.Options{TLSConfig: tlsCfg})
		if dialErr != nil {
			return nil, lastUID, fmt.Errorf("imap dial %s: %w", addr, dialErr)
		}
	}
	defer c.Close()

	if err := c.Login(username, password).Wait(); err != nil {
		return nil, lastUID, fmt.Errorf("imap login: %w", err)
	}

	mbox, err := c.Select("INBOX", nil).Wait()
	if err != nil {
		return nil, lastUID, fmt.Errorf("imap select INBOX: %w", err)
	}
	if mbox.NumMessages == 0 {
		return nil, lastUID, nil
	}

	var criteria imap.SearchCriteria
	if lastUID > 0 {
		var rangeSet imap.UIDSet
		rangeSet.AddRange(imap.UID(lastUID+1), 0) // 0 = * (highest)
		criteria.UID = []imap.UIDSet{rangeSet}
	} else {
		criteria.NotFlag = []imap.Flag{imap.FlagSeen}
	}

	searchData, err := c.UIDSearch(&criteria, nil).Wait()
	if err != nil || len(searchData.AllUIDs()) == 0 {
		return nil, lastUID, nil
	}

	uidSet := imap.UIDSetNum(searchData.AllUIDs()...)

	fetchOptions := &imap.FetchOptions{
		Envelope: true,
		UID:      true,
		BodySection: []*imap.FetchItemBodySection{
			{Specifier: imap.PartSpecifierText},
		},
	}

	// UIDSet implements NumSet — go-imap sends "UID FETCH" automatically.
	fetched, err := c.Fetch(uidSet, fetchOptions).Collect()
	if err != nil {
		return nil, lastUID, fmt.Errorf("imap fetch: %w", err)
	}

	for _, m := range fetched {
		uid := uint32(m.UID)
		if uid > maxUID {
			maxUID = uid
		}
		env := m.Envelope
		if env == nil || len(env.From) == 0 {
			continue
		}

		msg := Message{
			UID:       uid,
			MessageID: env.MessageID,
			Subject:   env.Subject,
			Date:      env.Date,
			FromEmail: env.From[0].Addr(),
			FromName:  strings.TrimSpace(env.From[0].Name),
		}
		if len(env.InReplyTo) > 0 {
			msg.InReplyTo = env.InReplyTo[0]
		}
		for _, a := range env.To {
			msg.ToList = append(msg.ToList, a.Addr())
		}
		for _, bs := range m.BodySection {
			msg.BodyText = strings.TrimSpace(string(bs.Bytes))
		}
		msgs = append(msgs, msg)
	}

	// Mark fetched messages seen using UID STORE.
	storeFlags := &imap.StoreFlags{
		Op:     imap.StoreFlagsAdd,
		Flags:  []imap.Flag{imap.FlagSeen},
		Silent: true,
	}
	_ = c.Store(uidSet, storeFlags, nil).Close()

	return msgs, maxUID, nil
}

// SendReply sends an authenticated SMTP reply.
// Uses implicit TLS on port 465, STARTTLS on all other ports.
func SendReply(smtpHost string, smtpPort int, username, password, from, to, subject, htmlBody string) error {
	var buf strings.Builder
	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", to)
	fmt.Fprintf(&buf, "Subject: %s\r\n", subject)
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
	buf.WriteString(htmlBody)

	addr := fmt.Sprintf("%s:%d", smtpHost, smtpPort)
	auth := smtp.PlainAuth("", username, password, smtpHost)

	if smtpPort == 465 {
		tlsCfg := &tls.Config{ServerName: smtpHost}
		conn, err := tls.Dial("tcp", addr, tlsCfg)
		if err != nil {
			return fmt.Errorf("smtp tls dial: %w", err)
		}
		c, err := smtp.NewClient(conn, smtpHost)
		if err != nil {
			conn.Close()
			return fmt.Errorf("smtp new client: %w", err)
		}
		defer c.Close()
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
		if err := c.Mail(from); err != nil {
			return err
		}
		if err := c.Rcpt(to); err != nil {
			return err
		}
		w, err := c.Data()
		if err != nil {
			return err
		}
		if _, err := fmt.Fprint(w, buf.String()); err != nil {
			return err
		}
		return w.Close()
	}

	return smtp.SendMail(addr, auth, from, []string{to}, []byte(buf.String()))
}
