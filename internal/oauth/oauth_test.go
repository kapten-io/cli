package oauth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kapten-io/cli/internal/dpop"
	"github.com/kapten-io/cli/internal/oauth"
	"github.com/kapten-io/cli/internal/oauth/oauthtest"
	"github.com/lestrrat-go/jwx/v3/jwk"
)

const clientID = "cli_9811a064-0835-43f4-a478-9ff95d92bc28"

// recordingSleep records the waits between polls without sleeping.
type recordingSleep struct{ waits []time.Duration }

func (r *recordingSleep) sleep(ctx context.Context, d time.Duration) error {
	r.waits = append(r.waits, d)

	return ctx.Err()
}

func newClient() (*oauth.Client, *recordingSleep) {
	sleep := &recordingSleep{}

	return &oauth.Client{Sleep: sleep.sleep}, sleep
}

func mustKey(t *testing.T) *dpop.Key {
	t.Helper()
	key, err := dpop.Generate()
	if err != nil {
		t.Fatal(err)
	}

	return key
}

func TestDeviceLoginAndRefreshRotation(t *testing.T) {
	server := oauthtest.New(t)
	server.PendingPolls, server.SlowDowns = 2, 1
	client, sleep := newClient()
	ctx := context.Background()
	metadata, err := client.Discover(ctx, server.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	key := mustKey(t)
	device, err := client.StartDevice(ctx, metadata, clientID, "", key)
	if err != nil {
		t.Fatal(err)
	}
	if device.UserCode != "WDJB-MJHT" || !strings.HasSuffix(device.VerificationURIComplete, "user_code=WDJB-MJHT") {
		t.Fatalf("device = %+v", device)
	}
	token, err := client.PollDevice(ctx, metadata, clientID, device, key)
	if err != nil {
		t.Fatal(err)
	}
	// slow_down first, then two pending polls, then the approval: the
	// interval grows by five seconds on slow_down and stays there.
	want := []time.Duration{5 * time.Second, 10 * time.Second, 10 * time.Second, 10 * time.Second}
	if len(sleep.waits) != len(want) {
		t.Fatalf("waits = %v", sleep.waits)
	}
	for i := range want {
		if sleep.waits[i] != want[i] {
			t.Fatalf("waits = %v, want %v", sleep.waits, want)
		}
	}
	if token.TokenType != "DPoP" || token.RefreshToken == "" || token.OrganizationID != server.Organization || token.Expiry.IsZero() {
		t.Fatalf("token = %+v", token)
	}
	claims, err := oauth.ReadClaims(token.AccessToken)
	if err != nil || claims.Subject != server.Subject || claims.OrganizationID != server.Organization || claims.Email != server.Email {
		t.Fatalf("claims = %+v %v", claims, err)
	}

	rotated, err := client.Refresh(ctx, metadata, clientID, token.RefreshToken, key)
	if err != nil || rotated.RefreshToken == token.RefreshToken {
		t.Fatalf("refresh = %+v %v", rotated, err)
	}
	// Another key cannot use the refresh token, and the reused one revokes
	// the family so the rotated one dies too.
	if _, err := client.Refresh(ctx, metadata, clientID, rotated.RefreshToken, mustKey(t)); !oauth.IsCode(err, "invalid_dpop_proof") {
		t.Fatalf("foreign key: %v", err)
	}
	if _, err := client.Refresh(ctx, metadata, clientID, token.RefreshToken, key); !oauth.IsCode(err, "invalid_grant") {
		t.Fatalf("reuse: %v", err)
	}
	if _, err := client.Refresh(ctx, metadata, clientID, rotated.RefreshToken, key); !oauth.IsCode(err, "invalid_grant") {
		t.Fatalf("family survived reuse: %v", err)
	}
	for _, jkt := range server.Proofs[:4] {
		if jkt != key.Thumbprint() {
			t.Fatal("token request signed with another key")
		}
	}
}

// RFC 8628 §3.5: an unavailable auth service is not an answer. The CLI backs
// off, doubling the interval, and the approval still logs in.
func TestDeviceKeepsPollingWhileAuthIsUnavailable(t *testing.T) {
	server := oauthtest.New(t)
	server.PendingPolls, server.Unavailable = 1, 2
	client, sleep := newClient()
	ctx := context.Background()
	metadata, err := client.Discover(ctx, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	key := mustKey(t)
	device, err := client.StartDevice(ctx, metadata, clientID, "", key)
	if err != nil {
		t.Fatal(err)
	}
	token, err := client.PollDevice(ctx, metadata, clientID, device, key)
	if err != nil || token.AccessToken == "" {
		t.Fatalf("poll = %+v %v", token, err)
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 20 * time.Second}
	if len(sleep.waits) != len(want) {
		t.Fatalf("waits = %v, want %v", sleep.waits, want)
	}
	for i := range want {
		if sleep.waits[i] != want[i] {
			t.Fatalf("waits = %v, want %v", sleep.waits, want)
		}
	}
}

// A token response cut off after its headers is transient too: the poll backs
// off and the next one logs in.
func TestDeviceKeepsPollingAfterATruncatedResponse(t *testing.T) {
	server := oauthtest.New(t)
	truncated := 1
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" && truncated > 0 {
			truncated--
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access`))
			return
		}
		server.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	server.URL = proxy.URL
	client, sleep := newClient()
	ctx := context.Background()
	metadata, err := client.Discover(ctx, proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	key := mustKey(t)
	device, err := client.StartDevice(ctx, metadata, clientID, "", key)
	if err != nil {
		t.Fatal(err)
	}
	token, err := client.PollDevice(ctx, metadata, clientID, device, key)
	if err != nil || token.AccessToken == "" {
		t.Fatalf("poll = %+v %v", token, err)
	}
	if want := []time.Duration{5 * time.Second, 10 * time.Second}; len(sleep.waits) != 2 || sleep.waits[1] != want[1] {
		t.Fatalf("waits = %v, want %v", sleep.waits, want)
	}
}

// A refused connection outside the device flow is reported with the host,
// so the user can tell a wrong URL from an outage.
func TestUnreachableAuthNamesTheHost(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	_, err := (&oauth.Client{}).Discover(context.Background(), url)
	if !oauth.IsTransient(err) || !strings.Contains(err.Error(), strings.TrimPrefix(url, "http://")) {
		t.Fatalf("err = %v", err)
	}
}

func TestDeviceStopsOnDenialAndExpiry(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*oauthtest.Server)
		code string
	}{
		{"denied", func(s *oauthtest.Server) { s.Deny = true }, oauth.ErrorAccessDenied},
		{"expired", func(s *oauthtest.Server) { s.Expire = true }, oauth.ErrorExpiredToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := oauthtest.New(t)
			server.PendingPolls = 1
			tc.set(server)
			client, sleep := newClient()
			ctx := context.Background()
			metadata, _ := client.Discover(ctx, server.URL)
			key := mustKey(t)
			device, err := client.StartDevice(ctx, metadata, clientID, "", key)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.PollDevice(ctx, metadata, clientID, device, key); !oauth.IsCode(err, tc.code) {
				t.Fatalf("err = %v", err)
			}
			if tc.name == "denied" && len(sleep.waits) != 2 {
				t.Fatalf("waits = %v", sleep.waits)
			}
		})
	}
}

func TestDeviceExpiresLocally(t *testing.T) {
	server := oauthtest.New(t)
	server.PendingPolls = 1 << 30
	client := &oauth.Client{Sleep: func(ctx context.Context, d time.Duration) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	ctx := context.Background()
	metadata, _ := client.Discover(ctx, server.URL)
	key := mustKey(t)
	device, _ := client.StartDevice(ctx, metadata, clientID, "", key)
	device.ExpiresIn = 1
	if _, err := client.PollDevice(ctx, metadata, clientID, device, key); !oauth.IsCode(err, oauth.ErrorExpiredToken) {
		t.Fatalf("err = %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := client.PollDevice(canceled, metadata, clientID, device, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled poll: %v", err)
	}
}

func TestClientCredentials(t *testing.T) {
	server := oauthtest.New(t)
	server.Applications["cli_app"] = "kapten_secret"
	client, _ := newClient()
	ctx := context.Background()
	metadata, _ := client.Discover(ctx, server.URL)
	token, err := client.ClientCredentials(ctx, metadata, "cli_app", "kapten_secret", "", mustKey(t))
	if err != nil || token.TokenType != "DPoP" || token.RefreshToken != "" {
		t.Fatalf("token = %+v %v", token, err)
	}
	_, err = client.ClientCredentials(ctx, metadata, "cli_app", "bogus-secret-value", "", mustKey(t))
	var oauthErr *oauth.Error
	if !errors.As(err, &oauthErr) || oauthErr.Code != "invalid_client" || oauthErr.Status != http.StatusUnauthorized {
		t.Fatalf("wrong secret: %v", err)
	}
	if strings.Contains(err.Error(), "bogus-secret-value") {
		t.Fatal("error echoes the secret")
	}
}

func TestDiscoveryChecksTheIssuer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"https://elsewhere.example","token_endpoint":"https://elsewhere.example/token"}`))
	}))
	defer server.Close()
	client, _ := newClient()
	if _, err := client.Discover(context.Background(), server.URL); err == nil || !strings.Contains(err.Error(), "elsewhere") {
		t.Fatalf("err = %v", err)
	}
	for _, bad := range []string{"", "ftp://auth.example", "https://auth.example?x=1", "https://user@auth.example"} {
		if _, err := client.Discover(context.Background(), bad); err == nil {
			t.Fatalf("issuer %q accepted", bad)
		}
	}
}

func TestResponsesAreBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 2<<20)))
	}))
	defer server.Close()
	client, _ := newClient()
	if _, err := client.Discover(context.Background(), server.URL); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientAssertion(t *testing.T) {
	for _, format := range []string{"jwk", "pem"} {
		t.Run(format, func(t *testing.T) {
			private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			public, _ := jwk.Import(private.Public())
			server := oauthtest.New(t)
			server.ApplicationKeys["cli_app"] = public
			var raw []byte
			if format == "jwk" {
				key, _ := jwk.Import(private)
				raw, _ = json.Marshal(key)
			} else {
				der, _ := x509.MarshalPKCS8PrivateKey(private)
				raw = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
			}
			key, err := oauth.ParsePrivateKey(raw)
			if err != nil {
				t.Fatal(err)
			}
			client, _ := newClient()
			ctx := context.Background()
			metadata, _ := client.Discover(ctx, server.URL)
			token, err := client.ClientAssertionToken(ctx, metadata, "cli_app", key, "", mustKey(t))
			if err != nil || token.TokenType != "DPoP" {
				t.Fatalf("token = %+v %v", token, err)
			}
			other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			wrong, _ := jwk.Import(other)
			if _, err := client.ClientAssertionToken(ctx, metadata, "cli_app", wrong, "", mustKey(t)); !oauth.IsCode(err, "invalid_client") {
				t.Fatalf("foreign key: %v", err)
			}
		})
	}
	if _, err := oauth.ParsePrivateKey([]byte(`{"kty":"EC","crv":"P-256","x":"f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU","y":"x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0"}`)); err == nil {
		t.Fatal("public key accepted")
	}
	if _, err := oauth.ParsePrivateKey([]byte("not a key")); err == nil {
		t.Fatal("garbage accepted")
	}
}
