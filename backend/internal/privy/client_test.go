package privy_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"tokenswoop/backend/internal/privy"
	"tokenswoop/backend/internal/testutil"
)

func TestVerifiedIdentityAndJWKSCache(t *testing.T) {
	f := testutil.NewPrivy(t, "0x"+strings.Repeat("AB", 20))
	for i := 0; i < 2; i++ {
		u, e := f.Client.Authenticate(context.Background(), f.Token(nil))
		if e != nil || u.ID != f.Profile.ID || u.Wallets()[0].Address != "0x"+strings.Repeat("ab", 20) {
			t.Fatal("valid identity rejected", e)
		}
	}
	if f.JWKSReads != 1 || f.UserReads != 2 {
		t.Fatal("keys should cache, current ownership should not", f.JWKSReads, f.UserReads)
	}
	f.Profile.Guest = true
	if _, e := f.Client.Authenticate(context.Background(), f.Token(nil)); !errors.Is(e, privy.ErrIdentity) {
		t.Fatal("guest accepted")
	}
	f.Profile.Guest = false
	f.Profile.FrozenAt = new(int64)
	if _, e := f.Client.Authenticate(context.Background(), f.Token(nil)); !errors.Is(e, privy.ErrIdentity) {
		t.Fatal("frozen identity accepted")
	}
}
func TestRejectsForgedExpiredAndCrossApplicationTokens(t *testing.T) {
	f := testutil.NewPrivy(t, "0x"+strings.Repeat("ab", 20))
	cases := []jwt.MapClaims{{"aud": "another-app"}, {"iss": "attacker"}, {"sub": "did:evil:me"}, {"sid": nil}, {"iat": nil}, {"exp": nil}, {"iat": time.Now().Add(time.Hour).Unix()}, {"exp": time.Now().Add(-time.Minute).Unix()}}
	for _, v := range cases {
		if _, e := f.Client.Authenticate(context.Background(), f.Token(v)); !errors.Is(e, privy.ErrIdentity) {
			t.Fatalf("invalid claims accepted: %v %v", v, e)
		}
	}
	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": f.Profile.ID})
	forged.Header["kid"] = "fixture-key"
	token, _ := forged.SignedString([]byte("forged"))
	for _, token := range []string{token, "not a jwt", strings.Repeat("a", 16385)} {
		if _, e := f.Client.Authenticate(context.Background(), token); !errors.Is(e, privy.ErrIdentity) {
			t.Fatal("forged token accepted", e)
		}
	}
	if f.UserReads != 0 {
		t.Fatal("invalid token fetched a profile")
	}
}
