package contextstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kapten-io/cli/internal/fileutil"
)

type Context struct {
	Name           string `json:"name"`
	Issuer         string `json:"issuer"`
	APIURL         string `json:"api_url"`
	Subject        string `json:"subject,omitempty"`
	Email          string `json:"email,omitempty"`
	OrganizationID string `json:"organization_id"`
	// OrganizationName is the name when the context was saved, for display.
	OrganizationName string `json:"organization_name,omitempty"`
	CredentialKey    string `json:"credential_key"`
	Storage          string `json:"storage"`
}
type Config struct {
	Version  int       `json:"version"`
	Active   string    `json:"active,omitempty"`
	Contexts []Context `json:"contexts"`
}

func Dir() string {
	if v := os.Getenv("KAPTEN_CONFIG_DIR"); v != "" {
		return v
	}
	d, _ := os.UserConfigDir()
	return filepath.Join(d, "kapten")
}
func Path() string            { return filepath.Join(Dir(), "config.json") }
func CredentialsPath() string { return filepath.Join(Dir(), "credentials.json") }

func Load() (Config, error) {
	data, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return Config{Version: 1}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err = json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if c.Version == 0 {
		c.Version = 1
	}
	if c.Version != 1 {
		return Config{}, fmt.Errorf("unsupported config version %d", c.Version)
	}
	return c, nil
}
func Save(c Config) error {
	if c.Version == 0 {
		c.Version = 1
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')
	if err := fileutil.WriteFile(Path(), data); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}
func (c Config) Find(name string) (Context, bool) {
	for _, x := range c.Contexts {
		if x.Name == name {
			return x, true
		}
	}
	return Context{}, false
}
func (c Config) FindIdentity(issuer, subject, org string) (int, bool) {
	for i, x := range c.Contexts {
		if x.Issuer == issuer && x.Subject == subject && x.OrganizationID == org {
			return i, true
		}
	}
	return 0, false
}
func UniqueName(c Config, desired string) string {
	desired = strings.TrimSpace(desired)
	if desired == "" {
		desired = "kapten"
	}
	if _, exists := c.Find(desired); !exists {
		return desired
	}
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s-%d", desired, i)
		if _, exists := c.Find(n); !exists {
			return n
		}
	}
}
func CredentialKey(issuer, subject, org, client string) string {
	h := sha256.Sum256([]byte(issuer + "\x00" + subject + "\x00" + org + "\x00" + client))
	return "oauth-" + hex.EncodeToString(h[:12])
}
