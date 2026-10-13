package faucet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

const Action = "kusdc_faucet"

var ErrVerification = errors.New("human verification failed")
var ErrVerificationUnavailable = errors.New("human verification unavailable")

type Verifier interface {
	Verify(context.Context, string, string, string) error
}
type Turnstile struct {
	Secret, Hostname string
	HTTP             *http.Client
}

func NewTurnstile(secret, hostname string) *Turnstile {
	return &Turnstile{Secret: secret, Hostname: hostname, HTTP: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (v *Turnstile) Verify(ctx context.Context, token, ip, id string) error {
	if v.Secret == "" || len(token) == 0 || len(token) > 2048 {
		return ErrVerification
	}
	// Retry the same validation safely, but give a fresh challenge its own key.
	// A claim ID alone would conflate a failed token with its replacement.
	verificationID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(id+"\x00"+token)).String()
	body, _ := json.Marshal(map[string]string{"secret": v.Secret, "response": token, "remoteip": ip, "idempotency_key": verificationID})
	req, err := http.NewRequestWithContext(ctx, "POST", "https://challenges.cloudflare.com/turnstile/v0/siteverify", bytes.NewReader(body))
	if err != nil {
		return ErrVerificationUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := v.HTTP.Do(req)
	if err != nil {
		return ErrVerificationUnavailable
	}
	defer res.Body.Close()
	var result struct {
		Success  bool      `json:"success"`
		Hostname string    `json:"hostname"`
		Action   string    `json:"action"`
		CData    string    `json:"cdata"`
		Time     time.Time `json:"challenge_ts"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 16384)).Decode(&result) != nil {
		return ErrVerificationUnavailable
	}
	if !result.Success || !strings.EqualFold(result.Hostname, v.Hostname) || result.Action != Action || result.CData != id || result.Time.Before(time.Now().Add(-5*time.Minute)) || result.Time.After(time.Now().Add(30*time.Second)) {
		return ErrVerification
	}
	return nil
}
