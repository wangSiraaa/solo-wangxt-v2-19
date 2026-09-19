package credential_test

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"license/internal/service/credential"
)

func TestIssueParseRoundTrip(t *testing.T) {
	c := credential.Claims{
		CheckoutID: 42, PoolID: 7, Employee: "alice", Mode: "OFFLINE",
		Nonce: "abc123", IssuedAt: 1000, ExpiresAt: 2000,
	}
	tok := credential.Issue(c, "secret")
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || parts[0] != "LIC1" {
		t.Fatalf("bad token shape: %q", tok)
	}
	got, err := credential.Parse(tok, "secret")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.CheckoutID != 42 || got.Nonce != "abc123" {
		t.Fatalf("claims mismatch: %+v", got)
	}
}

func TestWrongSecretRejected(t *testing.T) {
	tok := credential.Issue(credential.Claims{CheckoutID: 1, ExpiresAt: 1}, "real")
	if _, err := credential.Parse(tok, "fake"); !errors.Is(err, credential.ErrBadSignature) {
		t.Fatalf("want bad signature, got %v", err)
	}
}

func TestTamperedPayloadRejected(t *testing.T) {
	tok := credential.Issue(credential.Claims{CheckoutID: 1, Nonce: "n", ExpiresAt: 1}, "k")
	parts := strings.Split(tok, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	tampered := strings.Replace(tok, parts[1],
		base64.RawURLEncoding.EncodeToString(append(raw, 'x')), 1)
	if _, err := credential.Parse(tampered, "k"); !errors.Is(err, credential.ErrBadSignature) {
		t.Fatalf("tampered payload accepted: %v", err)
	}
}

func TestMalformed(t *testing.T) {
	for _, bad := range []string{"", "garbage", "LIC1.ab", "XXX.ab.cd", "LIC1.@@@.###"} {
		if _, err := credential.Parse(bad, "k"); err == nil {
			t.Fatalf("malformed token accepted: %q", bad)
		}
	}
}

func TestExpiry(t *testing.T) {
	c := &credential.Claims{ExpiresAt: 1000}
	if err := credential.VerifyExpiry(c, time.Unix(999, 0)); err != nil {
		t.Fatalf("before expiry should be valid: %v", err)
	}
	if err := credential.VerifyExpiry(c, time.Unix(1000, 0)); !errors.Is(err, credential.ErrExpired) {
		t.Fatalf("at/after expiry want expired, got %v", err)
	}
}
