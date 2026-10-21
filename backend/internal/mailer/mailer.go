package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

type Sender interface {
	Send(ctx context.Context, to, subject, html, text string) error
}

// LogMailer prints emails to the console. Used when RESEND_API_KEY is empty.
type LogMailer struct{}

func (LogMailer) Send(_ context.Context, to, subject, _, text string) error {
	log.Printf("[dev mail] to=%s subject=%q\n%s\n", to, subject, text)
	return nil
}

type Resend struct {
	apiKey string
	from   string
	client *http.Client
}

func NewResend(apiKey, from string) *Resend {
	return &Resend{apiKey: apiKey, from: from, client: &http.Client{Timeout: 10 * time.Second}}
}

func (r *Resend) Send(ctx context.Context, to, subject, html, text string) error {
	body, err := json.Marshal(map[string]any{
		"from": r.from, "to": []string{to}, "subject": subject, "html": html, "text": text,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("resend: status %d: %s", resp.StatusCode, b)
	}
	return nil
}