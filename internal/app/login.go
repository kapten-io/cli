package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/kapten-io/cli/internal/contextstore"
	"github.com/kapten-io/cli/internal/credentialstore"
	"github.com/kapten-io/cli/internal/dpop"
	"github.com/kapten-io/cli/internal/oauth"
)

func oauthClientID() string {
	return env("KAPTEN_OAUTH_CLIENT_ID", clientID)
}

// login signs in with the device authorization grant: the user approves a
// code in a browser, on this machine or another one, while the CLI polls.
func (a *App) login(ctx context.Context, opts loginOptions) error {
	if environmentSet() {
		return errors.New("KAPTEN_CLIENT_ID is set: commands authenticate as that OAuth application; unset it to log in")
	}
	name, noBrowser := opts.name, opts.noBrowser
	issuer := strings.TrimRight(first(opts.issuerURL, env("KAPTEN_ISSUER_URL", defaultIssuerURL)), "/")
	api, err := apiURL(first(opts.apiURL, env("KAPTEN_API_URL", defaultAPIURL)))
	if err != nil {
		return err
	}
	cid := oauthClientID()
	client := a.oauthClient(nil)
	metadata, err := client.Discover(ctx, issuer)
	if err != nil {
		return fmt.Errorf("%w (issuer %s; set another with --issuer-url)", err, issuer)
	}
	// A new key per login: the grant, its refresh tokens and access tokens
	// are bound to it, and it is saved with them.
	key, err := dpop.Generate()
	if err != nil {
		return err
	}
	device, err := client.StartDevice(ctx, metadata, cid, os.Getenv("KAPTEN_OAUTH_SCOPE"), key)
	if err != nil {
		return fmt.Errorf("start login: %w", err)
	}
	target := device.VerificationURIComplete
	if target == "" {
		target = device.VerificationURI
	}
	fmt.Fprintf(a.Out, "To log in, open this page in a browser:\n\n  %s\n\nand check that it shows the code %s.\n", target, device.UserCode)
	if !noBrowser {
		if opener, ok := a.browserOpener(); ok && opener(ctx, target) == nil {
			fmt.Fprintln(a.Out, "Your browser has been opened.")
		}
	}
	fmt.Fprintln(a.Out, "Waiting for approval...")
	token, err := client.PollDevice(ctx, metadata, cid, device, key)
	switch {
	case oauth.IsCode(err, oauth.ErrorAccessDenied):
		return errors.New("login denied in the browser")
	case oauth.IsCode(err, oauth.ErrorExpiredToken):
		return errors.New("the code expired before it was approved; run `kapten login` again")
	case err != nil:
		return fmt.Errorf("login: %w", err)
	}
	claims, err := oauth.ReadClaims(token.AccessToken)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	org := token.OrganizationID
	if org == "" {
		org = claims.OrganizationID
	}
	organization := a.organization(ctx, metadata.Issuer, org, token.AccessToken)
	if name == "" {
		name = organization.Slug
	}
	if name == "" && org != "" {
		name = "org-" + org[:min(8, len(org))]
	}
	encodedKey, err := key.Marshal()
	if err != nil {
		return err
	}

	c, err := contextstore.Load()
	if err != nil {
		return err
	}
	idx, exists := c.FindIdentity(metadata.Issuer, claims.Subject, org)
	if !exists {
		name = contextstore.UniqueName(c, name)
	}
	credentialKey := contextstore.CredentialKey(metadata.Issuer, claims.Subject, org, cid)
	saved := credentialstore.Token{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, TokenType: token.TokenType, DPoPKey: encodedKey}
	if !token.Expiry.IsZero() {
		saved.Expiry = token.Expiry.Unix()
	}
	cs := credentialstore.New("kapten", contextstore.CredentialsPath())
	backend, err := cs.Set(credentialKey, saved)
	if err != nil {
		return err
	}
	a.warnStorage(backend)
	x := contextstore.Context{Name: name, Issuer: metadata.Issuer, APIURL: api, Subject: claims.Subject, Email: claims.Email, OrganizationID: org, OrganizationName: organization.Name, CredentialKey: credentialKey, Storage: backend}
	if exists {
		x.Name = c.Contexts[idx].Name
		c.Contexts[idx] = x
	} else {
		c.Contexts = append(c.Contexts, x)
	}
	c.Active = x.Name
	if err = contextstore.Save(c); err != nil {
		return err
	}
	where := "without an organization"
	if org != "" {
		where = "to organization " + orgLabel(x.OrganizationName, org)
	}
	fmt.Fprintf(a.Out, "Logged in %s as %s (context %s, credentials in the %s).\n", where, emailOr(claims.Subject, claims.Email), x.Name, storageLabel(backend))
	return nil
}

type loginOptions struct {
	name, issuerURL, apiURL string
	noBrowser               bool
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// apiURL checks an Engine API base URL before it is saved in a context.
func apiURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("invalid API URL %q", raw)
	}
	return u.String(), nil
}

func orgLabel(name, id string) string {
	switch {
	case id == "":
		return "no organization"
	case name == "":
		return id
	default:
		return fmt.Sprintf("%s (%s)", name, id)
	}
}

func storageLabel(backend string) string {
	if backend == "file" {
		return "owner-only file " + contextstore.CredentialsPath()
	}

	return "OS keychain"
}

type organizationInfo struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// organization reads the name of an organization from auth, best effort:
// the CLI works with the id alone.
func (a *App) organization(ctx context.Context, issuer, id, access string) organizationInfo {
	info, _ := a.lookupOrganization(ctx, issuer, id, access)
	return info
}

// lookupOrganization reads the organization from auth, which also tells
// whether the access token is still accepted: the HTTP status, 0 when auth
// could not be asked.
func (a *App) lookupOrganization(ctx context.Context, issuer, id, access string) (organizationInfo, int) {
	if id == "" {
		return organizationInfo{}, 0
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/organizations/"+url.PathEscape(id), nil)
	if err != nil {
		return organizationInfo{}, 0
	}
	// auth, like the Engine, takes access tokens as Bearer tokens.
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return organizationInfo{}, 0
	}
	defer res.Body.Close()
	var info organizationInfo
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&info) != nil {
		return organizationInfo{}, res.StatusCode
	}

	return info, res.StatusCode
}

// browserOpener returns how to open a URL, if a browser can open here.
func (a *App) browserOpener() (func(context.Context, string) error, bool) {
	if a.OpenBrowser != nil {
		return a.OpenBrowser, true
	}
	if !canOpenBrowser(runtime.GOOS, os.Getenv) {
		return nil, false
	}

	return openBrowser, true
}

// canOpenBrowser is false over SSH, where the browser would open on the
// remote machine, and on Linux and BSD without a graphical session.
func canOpenBrowser(goos string, getenv func(string) string) bool {
	for _, name := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		if getenv(name) != "" {
			return false
		}
	}
	switch goos {
	case "darwin", "windows":
		return true
	default:
		return getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != ""
	}
}

func openBrowser(ctx context.Context, u string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.CommandContext(ctx, "open", u)
	case "windows":
		c = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", u)
	default:
		c = exec.CommandContext(ctx, "xdg-open", u)
	}
	// Run waits/reaps the launcher; a failure leaves the printed URL.
	return c.Run()
}

// identity is what whoami shows.
type identity struct {
	Source           string `json:"source"`
	Credential       string `json:"credential,omitempty"`
	Context          string `json:"context,omitempty"`
	Storage          string `json:"storage,omitempty"`
	Issuer           string `json:"issuer"`
	APIURL           string `json:"api_url"`
	Subject          string `json:"subject"`
	Email            string `json:"email,omitempty"`
	ClientID         string `json:"client_id,omitempty"`
	ClientName       string `json:"client_name,omitempty"`
	OrganizationID   string `json:"organization_id,omitempty"`
	OrganizationName string `json:"organization_name,omitempty"`
	ExpiresAt        string `json:"expires_at,omitempty"`
}

// whoami shows who commands run as, getting a valid token first so the
// answer reflects what the next command will send.
func (a *App) whoami(ctx context.Context, contextName string, jsonOutput bool) error {
	client := a.oauthClient(nil)
	var who identity
	var access string
	var refresh func() (string, error)
	creds, err := envCredentials()
	if err != nil && contextName == "" {
		return err
	}
	if creds != nil && contextName == "" {
		token, _, err := creds.accessToken(ctx, client, false)
		if err != nil {
			return err
		}
		access = token
		refresh = func() (string, error) {
			token, _, err := creds.accessToken(ctx, client, true)
			return token, err
		}
		creds.mu.Lock()
		claims := creds.claims
		creds.mu.Unlock()
		who = identity{Source: "environment", Credential: creds.source(), Issuer: creds.issuer, APIURL: env("KAPTEN_API_URL", defaultAPIURL), Subject: claims.Subject, ClientID: creds.clientID, ClientName: claims.ClientName, OrganizationID: claims.OrganizationID}
	} else {
		selected, err := selectedContext(contextName)
		if err != nil {
			return err
		}
		store := credentialstore.New("kapten", contextstore.CredentialsPath())
		token, _, err := a.storedAccessToken(ctx, client, store, &selected, false)
		if err != nil {
			return err
		}
		access = token
		refresh = func() (string, error) {
			token, _, err := a.storedAccessToken(ctx, client, store, &selected, true)
			return token, err
		}
		who = identity{Source: "login", Context: selected.Name, Storage: selected.Storage, Issuer: selected.Issuer, APIURL: selected.APIURL, Subject: selected.Subject, Email: selected.Email, OrganizationID: selected.OrganizationID, OrganizationName: selected.OrganizationName}
	}
	claims, _ := oauth.ReadClaims(access)
	if claims.OrganizationID != "" {
		who.OrganizationID = claims.OrganizationID
	}
	if claims.ExpiresAt > 0 {
		who.ExpiresAt = time.Unix(claims.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	// Asking auth for the organization also checks the session is alive: a
	// refused token is refreshed once, as a command would, and a refused
	// refresh means logging in again.
	info, status := a.lookupOrganization(ctx, who.Issuer, who.OrganizationID, access)
	if status == http.StatusUnauthorized {
		if access, err = refresh(); err != nil {
			return err
		}
		info, _ = a.lookupOrganization(ctx, who.Issuer, who.OrganizationID, access)
	}
	if info.Name != "" {
		who.OrganizationName = info.Name
	}
	if jsonOutput {
		return json.NewEncoder(a.Out).Encode(who)
	}
	if who.Source == "environment" {
		label := who.ClientID
		if who.ClientName != "" {
			label = fmt.Sprintf("%s (%s)", who.ClientName, who.ClientID)
		}
		fmt.Fprintf(a.Out, "OAuth application %s\n", label)
	} else {
		fmt.Fprintf(a.Out, "User %s\n", emailOr(who.Subject, who.Email))
	}
	fmt.Fprintf(a.Out, "Organization %s\n", orgLabel(who.OrganizationName, who.OrganizationID))
	if who.Source == "environment" {
		fmt.Fprintf(a.Out, "From %s, token kept in memory\n", who.Credential)
	} else {
		fmt.Fprintf(a.Out, "From context %s, credentials in the %s\n", who.Context, storageLabel(who.Storage))
	}
	fmt.Fprintf(a.Out, "Issuer %s\n", who.Issuer)
	fmt.Fprintf(a.Out, "API %s\n", who.APIURL)
	if who.ExpiresAt != "" {
		fmt.Fprintf(a.Out, "Access token valid until %s\n", who.ExpiresAt)
	}

	return nil
}
