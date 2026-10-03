package authn

import (
	"testing"
	"time"
)

func TestSignParseRoundTrip(t *testing.T) {
	s := NewSigner("secret", time.Hour)
	tok, err := s.Sign("user-1", "tenant-1", RoleCustomer, "access", 0)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Parse(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "user-1" || c.TenantID != "tenant-1" || c.Role != RoleCustomer {
		t.Fatalf("claims mismatch: %+v", c)
	}
}

func TestWrongSecretRejected(t *testing.T) {
	a := NewSigner("secret-a", time.Hour)
	b := NewSigner("secret-b", time.Hour)
	tok, _ := a.Sign("u", "t", RoleStaff, "access", 0)
	if _, err := b.Parse(tok); err == nil {
		t.Fatal("token signed with another secret must be rejected")
	}
}

func TestAccessTokenOnly(t *testing.T) {
	s := NewSigner("secret", time.Hour)
	refresh, _ := s.Sign("u", "t", RoleCustomer, "refresh", 0)
	if _, err := s.Parse(refresh); err == nil {
		t.Fatal("refresh token must not parse as access token")
	}
	claims, err := s.ParseAny(refresh)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Type != "refresh" {
		t.Fatalf("type = %q", claims.Type)
	}
}

func TestExpiredRejected(t *testing.T) {
	s := NewSigner("secret", time.Hour)
	tok, _ := s.Sign("u", "t", RoleCustomer, "access", time.Nanosecond)
	time.Sleep(time.Millisecond) // let the 1ns validity lapse
	if _, err := s.Parse(tok); err == nil {
		t.Fatal("expired token must be rejected")
	}
}
