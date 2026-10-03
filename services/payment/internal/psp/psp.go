// Package psp isolates external payment-provider integration behind a
// small interface. The platform never sees card data: only tokens issued
// by the provider's hosted fields / SDK reach this service.
package psp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

type AuthorizeRequest struct {
	Token       string
	AmountCents int64
	Currency    string
	// IdemKey is passed to the provider so network retries never double-charge.
	IdemKey string
}

type AuthorizeResult struct {
	OK            bool
	PspRef        string
	Brand         string
	Last4         string
	FailureReason string
	Captured      bool
}

type PSP interface {
	Name() string
	Authorize(ctx context.Context, req AuthorizeRequest) (AuthorizeResult, error)
	Capture(ctx context.Context, pspRef, idemKey string) error
	Void(ctx context.Context, pspRef string) error
	VerifyWebhook(signature string, body []byte) bool
}

// Fake is the deterministic dev/test provider. Tokens containing
// "decline" fail authorization; everything else succeeds.
type Fake struct {
	secret []byte
}

func NewFake(secret string) *Fake {
	return &Fake{secret: []byte(secret)}
}

func (f *Fake) Name() string { return "fake" }

func (f *Fake) Authorize(_ context.Context, req AuthorizeRequest) (AuthorizeResult, error) {
	if strings.Contains(req.Token, "decline") {
		return AuthorizeResult{
			OK: false, FailureReason: "card_declined",
			PspRef: "fake_" + randHex(),
		}, nil
	}
	brand, last4 := "visa", "4242"
	switch {
	case strings.Contains(req.Token, "mc"):
		brand, last4 = "mastercard", "5454"
	case strings.Contains(req.Token, "amex"):
		brand, last4 = "amex", "0005"
	}
	return AuthorizeResult{
		OK: true, PspRef: "fake_" + randHex(),
		Brand: brand, Last4: last4, Captured: true,
	}, nil
}

func (f *Fake) Capture(_ context.Context, _, _ string) error { return nil }
func (f *Fake) Void(_ context.Context, _ string) error       { return nil }

// VerifyWebhook checks an HMAC-SHA256 hex signature over the raw body.
func (f *Fake) VerifyWebhook(signature string, body []byte) bool {
	mac := hmac.New(sha256.New, f.secret)
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(strings.ToLower(signature)), []byte(expected))
}

func randHex() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
