// Package dpop holds the CLI's proof-of-possession key and signs DPoP proofs
// (RFC 9449). auth binds every token it issues to the CLI to this key: the
// device code, the refresh tokens and the access tokens all need a proof
// signed by it.
package dpop

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
)

// Header is the request header carrying a proof.
const Header = "DPoP"

const proofType = "dpop+jwt"

// Key is an ES256 key pair.
type Key struct {
	private *ecdsa.PrivateKey
	public  jwk.Key
	jkt     string
}

// Generate draws a new P-256 key pair.
func Generate() (*Key, error) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate DPoP key: %w", err)
	}

	return newKey(private)
}

// Parse reads a key saved with Marshal.
func Parse(raw string) (*Key, error) {
	key, err := jwk.ParseKey([]byte(raw))
	if err != nil {
		return nil, errors.New("parse DPoP key: invalid JWK")
	}
	var private ecdsa.PrivateKey
	if err := jwk.Export(key, &private); err != nil || private.Curve != elliptic.P256() {
		return nil, errors.New("parse DPoP key: not a P-256 private key")
	}

	return newKey(&private)
}

func newKey(private *ecdsa.PrivateKey) (*Key, error) {
	public, err := jwk.Import(private.Public())
	if err != nil {
		return nil, fmt.Errorf("import DPoP public key: %w", err)
	}
	thumbprint, err := public.Thumbprint(crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("compute DPoP key thumbprint: %w", err)
	}

	return &Key{private: private, public: public, jkt: base64.RawURLEncoding.EncodeToString(thumbprint)}, nil
}

// Marshal encodes the private key as a JWK, for the credential store.
func (k *Key) Marshal() (string, error) {
	key, err := jwk.Import(k.private)
	if err != nil {
		return "", fmt.Errorf("export DPoP key: %w", err)
	}
	raw, err := json.Marshal(key)
	if err != nil {
		return "", fmt.Errorf("encode DPoP key: %w", err)
	}

	return string(raw), nil
}

// Thumbprint is the RFC 7638 SHA-256 thumbprint of the public key, the
// dpop_jkt of a device authorization.
func (k *Key) Thumbprint() string {
	return k.jkt
}

// Proof signs a proof for one request. accessToken is the token the request
// carries, bound into the proof by its hash (ath); empty on token requests.
// Each call returns a new proof: auth refuses a jti it has already seen.
func (k *Key) Proof(method, target, accessToken string, now time.Time) (string, error) {
	u, err := url.Parse(target)
	if err != nil {
		return "", errors.New("DPoP proof: invalid request URL")
	}
	// htu excludes the query and fragment (RFC 9449 section 4.2).
	u.RawQuery, u.Fragment, u.User = "", "", nil
	jti := make([]byte, 16)
	_, _ = rand.Read(jti)
	claims := map[string]any{
		"jti": base64.RawURLEncoding.EncodeToString(jti),
		"htm": method,
		"htu": u.String(),
		"iat": now.Unix(),
	}
	if accessToken != "" {
		hash := sha256.Sum256([]byte(accessToken))
		claims["ath"] = base64.RawURLEncoding.EncodeToString(hash[:])
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode DPoP proof: %w", err)
	}
	headers := jws.NewHeaders()
	if err := headers.Set(jws.TypeKey, proofType); err != nil {
		return "", err
	}
	if err := headers.Set(jws.JWKKey, k.public); err != nil {
		return "", err
	}
	signed, err := jws.Sign(payload, jws.WithKey(jwa.ES256(), k.private, jws.WithProtectedHeaders(headers)))
	if err != nil {
		return "", fmt.Errorf("sign DPoP proof: %w", err)
	}

	return string(signed), nil
}
