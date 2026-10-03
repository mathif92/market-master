// Package dispatch abstracts how orders reach customers: the market's own
// fleet or a third-party logistics provider (3PL).
package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

type Request struct {
	OrderID       string
	MethodCode    string
	RecipientName string
	AddressLine   string
	City          string
	Country       string
	PostalCode    string
	// IdempotencyKey must be deterministic for the order (we derive it as
	// "dispatch:"+order_id), so a retried dispatch never creates a second
	// parcel with a real provider.
	IdempotencyKey string
}

type Result struct {
	ProviderRef string
	TrackingURL string
}

type Dispatcher interface {
	Name() string
	CreateDispatch(ctx context.Context, req Request) (Result, error)
	CancelDispatch(ctx context.Context, providerRef string) error
}

// OwnFleet is the market's own courier operation. Refs are derived from
// the idempotency key, making CreateDispatch naturally idempotent.
type OwnFleet struct{}

func (OwnFleet) Name() string { return "own_fleet" }

func (OwnFleet) CreateDispatch(_ context.Context, req Request) (Result, error) {
	ref := "own-" + shortHash(req.IdempotencyKey)
	return Result{
		ProviderRef: ref,
		TrackingURL: fmt.Sprintf("https://fleet.local/track/%s", ref),
	}, nil
}

func (OwnFleet) CancelDispatch(_ context.Context, _ string) error { return nil }

// ThirdParty is a stub adapter for an external 3PL ("FastShip"). It mirrors
// the contract a real integration needs: deterministic refs under retry,
// cancel support, and a tracking URL.
type ThirdParty struct {
	APIKey string
	Base   string
}

func NewThirdParty(apiKey string) *ThirdParty {
	return &ThirdParty{APIKey: apiKey, Base: "https://fastship.example"}
}

func (t *ThirdParty) Name() string { return "third_party" }

func (t *ThirdParty) CreateDispatch(_ context.Context, req Request) (Result, error) {
	// A real adapter would POST /dispatches with Idempotency-Key: req.IdempotencyKey.
	ref := "fs-" + shortHash(req.IdempotencyKey)
	return Result{
		ProviderRef: ref,
		TrackingURL: fmt.Sprintf("%s/track/%s", t.Base, ref),
	}, nil
}

func (t *ThirdParty) CancelDispatch(_ context.Context, providerRef string) error {
	// A real adapter would DELETE /dispatches/{providerRef}; 404 is fine
	// (already cancelled) and must not fail the local transition.
	_ = providerRef
	return nil
}

// Registry resolves a dispatcher by name with a safe default.
type Registry struct {
	byName map[string]Dispatcher
}

func NewRegistry(ds ...Dispatcher) *Registry {
	r := &Registry{byName: make(map[string]Dispatcher, len(ds))}
	for _, d := range ds {
		r.byName[d.Name()] = d
	}
	return r
}

func (r *Registry) Get(name string) Dispatcher {
	if d, ok := r.byName[name]; ok {
		return d
	}
	if d, ok := r.byName["own_fleet"]; ok {
		return d
	}
	return OwnFleet{}
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}
