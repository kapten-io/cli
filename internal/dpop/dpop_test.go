package dpop

import (
	"crypto"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
)

// verify checks a proof the way auth does (auth/internal/oauth/dpop.go).
func verify(t *testing.T, proof string) (map[string]any, string) {
	t.Helper()
	message, err := jws.Parse([]byte(proof), jws.WithCompact())
	if err != nil || len(message.Signatures()) != 1 {
		t.Fatalf("parse proof: %v", err)
	}
	headers := message.Signatures()[0].ProtectedHeaders()
	if typ, _ := headers.Type(); typ != "dpop+jwt" {
		t.Fatalf("typ = %q", typ)
	}
	alg, ok := headers.Algorithm()
	if !ok || alg.String() != "ES256" {
		t.Fatalf("alg = %v", alg)
	}
	key, ok := headers.JWK()
	if !ok {
		t.Fatal("no jwk header")
	}
	if private, err := jwk.IsPrivateKey(key); err != nil || private {
		t.Fatal("proof leaks the private key")
	}
	payload, err := jws.Verify([]byte(proof), jws.WithKey(alg, key))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	thumbprint, err := key.Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}

	return claims, base64.RawURLEncoding.EncodeToString(thumbprint)
}

func TestProof(t *testing.T) {
	key, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	proof, err := key.Proof("POST", "https://auth.example/token?ignored=1#frag", "", now)
	if err != nil {
		t.Fatal(err)
	}
	claims, jkt := verify(t, proof)
	if jkt != key.Thumbprint() {
		t.Fatalf("thumbprint %s, header key %s", key.Thumbprint(), jkt)
	}
	if claims["htm"] != "POST" || claims["htu"] != "https://auth.example/token" || claims["iat"] != float64(now.Unix()) || claims["jti"] == "" {
		t.Fatalf("claims = %v", claims)
	}
	if _, ok := claims["ath"]; ok {
		t.Fatal("token request proof carries ath")
	}

	again, _ := key.Proof("POST", "https://auth.example/token", "", now)
	if second, _ := verify(t, again); second["jti"] == claims["jti"] {
		t.Fatal("jti reused")
	}

	withToken, _ := key.Proof("GET", "https://api.example/projects", "access", now)
	resource, _ := verify(t, withToken)
	hash := sha256.Sum256([]byte("access"))
	if resource["ath"] != base64.RawURLEncoding.EncodeToString(hash[:]) {
		t.Fatalf("ath = %v", resource["ath"])
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	key, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := key.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Thumbprint() != key.Thumbprint() {
		t.Fatal("thumbprint changed through the store")
	}
	proof, _ := parsed.Proof("POST", "https://auth.example/token", "", time.Now())
	if _, jkt := verify(t, proof); jkt != key.Thumbprint() {
		t.Fatal("parsed key signs with another key")
	}
	for _, bad := range []string{"", "{}", `{"kty":"oct","k":"AAAA"}`} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("Parse(%q) accepted", bad)
		}
	}
}
