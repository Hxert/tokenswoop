package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

var WalletPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

func ChecksumWallet(wallet string) string {
	lower := strings.ToLower(strings.TrimPrefix(wallet, "0x"))
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte(lower))
	digest := hex.EncodeToString(h.Sum(nil))
	out := []byte(lower)
	for i, ch := range out {
		if ch >= 'a' && ch <= 'f' && digest[i] >= '8' {
			out[i] = ch - 32
		}
	}
	return "0x" + string(out)
}

func Token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func Digest(secret []byte, purpose, value string) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(purpose + ":" + value))
	return hex.EncodeToString(h.Sum(nil))
}
func Encrypt(secret []byte, plain string) (string, error) {
	b, e := aes.NewCipher(secret)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return "", e
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return "", e
	}
	return base64.StdEncoding.EncodeToString(g.Seal(nonce, nonce, []byte(plain), []byte("tokenswoop-channel-v1"))), nil
}
func Decrypt(secret []byte, value string) (string, error) {
	raw, e := base64.StdEncoding.DecodeString(value)
	if e != nil {
		return "", e
	}
	b, e := aes.NewCipher(secret)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(b)
	if e != nil || len(raw) < g.NonceSize() {
		return "", fmt.Errorf("invalid encrypted credential")
	}
	plain, e := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], []byte("tokenswoop-channel-v1"))
	return string(plain), e
}
func PersonalHash(message string) []byte {
	h := sha3.NewLegacyKeccak256()
	fmt.Fprintf(h, "\x19Ethereum Signed Message:\n%d%s", len([]byte(message)), message)
	return h.Sum(nil)
}
func VerifyWallet(wallet, message, signature string) bool {
	sig, err := hex.DecodeString(strings.TrimPrefix(signature, "0x"))
	if err != nil || len(sig) != 65 {
		return false
	}
	v := sig[64]
	if v >= 27 {
		v -= 27
	}
	if v > 1 {
		return false
	}
	compact := append([]byte{27 + v}, sig[:64]...)
	pub, _, err := ecdsa.RecoverCompact(compact, PersonalHash(message))
	if err != nil {
		return false
	}
	h := sha3.NewLegacyKeccak256()
	h.Write(pub.SerializeUncompressed()[1:])
	address := "0x" + hex.EncodeToString(h.Sum(nil)[12:])
	return strings.EqualFold(wallet, address)
}
