package credentialstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/kapten-io/cli/internal/fileutil"
	"github.com/zalando/go-keyring"
)

type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	Expiry       int64  `json:"expiry,omitempty"`
	// DPoPKey is the private JWK the tokens are bound to (see package dpop).
	DPoPKey string `json:"dpop_key,omitempty"`
}
type Store struct {
	service string
	file    string
	mu      sync.Mutex
}

func New(service, file string) *Store { return &Store{service: service, file: file} }

// FileOnly reports whether KAPTEN_CREDENTIALS_STORE=file keeps credentials
// out of the OS keychain, as in containers and CI where none runs.
func FileOnly() bool { return os.Getenv("KAPTEN_CREDENTIALS_STORE") == "file" }

func (s *Store) Get(key string) (Token, string, error) {
	if FileOnly() {
		t, err := s.getFile(key)
		if err != nil {
			return Token{}, "", err
		}
		return t, "file", nil
	}
	if raw, err := keyring.Get(s.service, key); err == nil {
		var t Token
		if json.Unmarshal([]byte(raw), &t) == nil {
			return t, "keychain", nil
		}
	}
	t, err := s.getFile(key)
	if err != nil {
		return Token{}, "", err
	}
	return t, "file", nil
}
func (s *Store) Set(key string, t Token) (string, error) {
	data, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("encode credentials: %w", err)
	}
	if FileOnly() {
		if err := s.setFile(key, t); err != nil {
			return "", err
		}
		return "file", nil
	}
	if err := keyring.Set(s.service, key, string(data)); err == nil {
		return "keychain", nil
	}
	if err := s.setFile(key, t); err != nil {
		return "", err
	}
	return "file", nil
}
func (s *Store) Delete(key string) error {
	if !FileOnly() {
		_ = keyring.Delete(s.service, key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.loadFile()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	delete(m, key)
	return s.saveFile(m)
}
func (s *Store) getFile(key string) (Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.loadFile()
	if err != nil {
		return Token{}, err
	}
	t, ok := m[key]
	if !ok {
		return Token{}, os.ErrNotExist
	}
	return t, nil
}
func (s *Store) setFile(key string, t Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.loadFile()
	if errors.Is(err, os.ErrNotExist) {
		m = map[string]Token{}
	} else if err != nil {
		return err
	}
	m[key] = t
	return s.saveFile(m)
}
func (s *Store) loadFile() (map[string]Token, error) {
	b, err := os.ReadFile(s.file)
	if err != nil {
		return nil, err
	}
	var m map[string]Token
	if err = json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	if m == nil {
		return nil, errors.New("parse credentials: expected an object")
	}
	return m, nil
}
func (s *Store) saveFile(m map[string]Token) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credential file: %w", err)
	}
	if err := fileutil.WriteFile(s.file, data); err != nil {
		return fmt.Errorf("save credential file: %w", err)
	}
	return nil
}
