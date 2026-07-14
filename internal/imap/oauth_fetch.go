package imap

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// OAuth-provider mailbox fetchers (COM-EM-003): Gmail (REST, history-id
// cursor) and Microsoft Graph (delta-link cursor). API bases are
// env-overridable so the sync path is fixture-testable locally; live
// round-trips are the credentialed certification step.

func gmailBase() string {
	if v := os.Getenv("GMAIL_API_BASE"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://gmail.googleapis.com/gmail/v1"
}

func graphBase() string {
	if v := os.Getenv("MSGRAPH_API_BASE"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://graph.microsoft.com/v1.0"
}

var oauthHTTP = &http.Client{Timeout: 30 * time.Second}

func getJSON(accessToken, rawURL string, out interface{}) error {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("unauthorized (%d): %s", resp.StatusCode, string(body[:min(200, len(body))]))
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("provider api %d: %s", resp.StatusCode, string(body[:min(200, len(body))]))
	}
	return json.Unmarshal(body, out)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------- Gmail ----------

// FetchNewGmail returns inbox messages newer than the cursor. The cursor is
// Gmail's historyId; an empty cursor bootstraps from the current profile
// historyId WITHOUT importing old mail (first sync is forward-only, matching
// the IMAP UID semantics).
func FetchNewGmail(accessToken, cursor string) (msgs []Message, nextCursor string, err error) {
	base := gmailBase()
	var profile struct {
		HistoryID string `json:"historyId"`
	}
	if err := getJSON(accessToken, base+"/users/me/profile", &profile); err != nil {
		return nil, cursor, err
	}
	if cursor == "" {
		return nil, profile.HistoryID, nil // bootstrap: start watching from now
	}

	var history struct {
		History []struct {
			MessagesAdded []struct {
				Message struct {
					ID string `json:"id"`
				} `json:"message"`
			} `json:"messagesAdded"`
		} `json:"history"`
		HistoryID string `json:"historyId"`
	}
	q := url.Values{}
	q.Set("startHistoryId", cursor)
	q.Set("historyTypes", "messageAdded")
	q.Set("labelId", "INBOX")
	if err := getJSON(accessToken, base+"/users/me/history?"+q.Encode(), &history); err != nil {
		// A 404 here means the cursor is too old — re-bootstrap forward.
		if strings.Contains(err.Error(), "404") {
			return nil, profile.HistoryID, nil
		}
		return nil, cursor, err
	}
	seen := map[string]bool{}
	for _, h := range history.History {
		for _, ma := range h.MessagesAdded {
			id := ma.Message.ID
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			msg, err := fetchGmailMessage(accessToken, base, id)
			if err != nil {
				return msgs, cursor, err
			}
			msgs = append(msgs, msg)
		}
	}
	next := history.HistoryID
	if next == "" {
		next = profile.HistoryID
	}
	return msgs, next, nil
}

func fetchGmailMessage(accessToken, base, id string) (Message, error) {
	var m struct {
		ID           string `json:"id"`
		InternalDate string `json:"internalDate"`
		Payload      struct {
			Headers []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"headers"`
			Body struct {
				Data string `json:"data"`
			} `json:"body"`
			Parts []struct {
				MimeType string `json:"mimeType"`
				Body     struct {
					Data string `json:"data"`
				} `json:"body"`
			} `json:"parts"`
		} `json:"payload"`
	}
	if err := getJSON(accessToken, base+"/users/me/messages/"+id+"?format=full", &m); err != nil {
		return Message{}, err
	}
	out := Message{MessageID: m.ID, Date: time.Now()}
	for _, h := range m.Payload.Headers {
		switch strings.ToLower(h.Name) {
		case "subject":
			out.Subject = h.Value
		case "from":
			out.FromName, out.FromEmail = splitAddress(h.Value)
		case "to":
			out.ToList = splitList(h.Value)
		case "message-id":
			out.MessageID = h.Value
		case "in-reply-to":
			out.InReplyTo = h.Value
		case "date":
			if t, err := time.Parse(time.RFC1123Z, h.Value); err == nil {
				out.Date = t
			}
		}
	}
	body := m.Payload.Body.Data
	if body == "" {
		for _, p := range m.Payload.Parts {
			if p.MimeType == "text/plain" && p.Body.Data != "" {
				body = p.Body.Data
				break
			}
		}
		if body == "" {
			for _, p := range m.Payload.Parts {
				if p.Body.Data != "" {
					body = p.Body.Data
					break
				}
			}
		}
	}
	if body != "" {
		if raw, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(strings.TrimRight(body, "=")); err == nil {
			out.BodyText = string(raw)
		}
	}
	return out, nil
}

// ---------- Microsoft Graph ----------

// FetchNewGraph returns inbox messages via the Graph delta query. The cursor
// is the deltaLink; an empty cursor bootstraps a delta round WITHOUT
// importing history (first call consumes existing pages and stores the
// resulting deltaLink).
func FetchNewGraph(accessToken, cursor string) (msgs []Message, nextCursor string, err error) {
	u := cursor
	bootstrap := false
	if u == "" {
		bootstrap = true
		u = graphBase() + "/me/mailFolders/inbox/messages/delta?$select=subject,from,toRecipients,receivedDateTime,bodyPreview,body,internetMessageId,conversationId"
	}
	for {
		var page struct {
			Value []struct {
				InternetMessageID string `json:"internetMessageId"`
				Subject           string `json:"subject"`
				ReceivedDateTime  string `json:"receivedDateTime"`
				BodyPreview       string `json:"bodyPreview"`
				Body              struct {
					ContentType string `json:"contentType"`
					Content     string `json:"content"`
				} `json:"body"`
				From struct {
					EmailAddress struct {
						Name    string `json:"name"`
						Address string `json:"address"`
					} `json:"emailAddress"`
				} `json:"from"`
				ToRecipients []struct {
					EmailAddress struct {
						Address string `json:"address"`
					} `json:"emailAddress"`
				} `json:"toRecipients"`
			} `json:"value"`
			NextLink  string `json:"@odata.nextLink"`
			DeltaLink string `json:"@odata.deltaLink"`
		}
		if err := getJSON(accessToken, u, &page); err != nil {
			return msgs, cursor, err
		}
		if !bootstrap {
			for _, v := range page.Value {
				msg := Message{
					MessageID: v.InternetMessageID,
					Subject:   v.Subject,
					FromEmail: strings.ToLower(v.From.EmailAddress.Address),
					FromName:  v.From.EmailAddress.Name,
					BodyText:  v.BodyPreview,
					Date:      time.Now(),
				}
				if v.Body.ContentType == "text" && v.Body.Content != "" {
					msg.BodyText = v.Body.Content
				}
				for _, tr := range v.ToRecipients {
					msg.ToList = append(msg.ToList, strings.ToLower(tr.EmailAddress.Address))
				}
				if t, err := time.Parse(time.RFC3339, v.ReceivedDateTime); err == nil {
					msg.Date = t
				}
				msgs = append(msgs, msg)
			}
		}
		if page.NextLink != "" {
			u = page.NextLink
			continue
		}
		if page.DeltaLink != "" {
			return msgs, page.DeltaLink, nil
		}
		return msgs, cursor, nil
	}
}

func splitAddress(v string) (name, email string) {
	v = strings.TrimSpace(v)
	if i := strings.LastIndex(v, "<"); i >= 0 && strings.HasSuffix(v, ">") {
		return strings.Trim(strings.TrimSpace(v[:i]), `"`), strings.ToLower(v[i+1 : len(v)-1])
	}
	return "", strings.ToLower(v)
}

func splitList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		_, addr := splitAddress(p)
		if addr != "" {
			out = append(out, addr)
		}
	}
	return out
}
