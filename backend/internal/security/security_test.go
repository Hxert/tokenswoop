package security_test

import (
	"strings"
	"testing"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/testutil"
)

func TestSignatureBindsWalletAndMessage(t *testing.T) {
	key, wallet := testutil.Wallet(t)
	message := "Login with nonce 12345"
	signature := testutil.Sign(key, message)
	if !security.VerifyWallet(wallet, message, signature) {
		t.Fatal("valid signature rejected")
	}
	_, other := testutil.Wallet(t)
	for _, v := range []struct{ wallet, message, sig string }{{other, message, signature}, {wallet, message + "changed", signature}, {wallet, message, signature[:50]}, {wallet, message, strings.Repeat("f", 130)}} {
		if security.VerifyWallet(v.wallet, v.message, v.sig) {
			t.Fatal("invalid signature accepted")
		}
	}
}
func TestCredentialsAuthenticatedEncryptionAndSeparatedDigests(t *testing.T) {
	secret := []byte(strings.Repeat("a", 32))
	a, e := security.Encrypt(secret, "sensitive-api-key")
	if e != nil {
		t.Fatal(e)
	}
	b, _ := security.Encrypt(secret, "sensitive-api-key")
	if a == b || strings.Contains(a, "sensitive") {
		t.Fatal("encryption nonce or secrecy failed")
	}
	plain, e := security.Decrypt(secret, a)
	if e != nil || plain != "sensitive-api-key" {
		t.Fatal("decrypt failed")
	}
	if _, e = security.Decrypt([]byte(strings.Repeat("b", 32)), a); e == nil {
		t.Fatal("wrong key accepted")
	}
	if _, e = security.Decrypt(secret, a[:len(a)-4]+"AAAA"); e == nil {
		t.Fatal("tampering accepted")
	}
	if security.Digest(secret, "session", "same") == security.Digest(secret, "api-key", "same") {
		t.Fatal("digest domains collide")
	}
}
