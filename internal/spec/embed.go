package spec

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kapten-io/cli/internal/fileutil"
)

//go:embed openapi.yml
var OpenAPI []byte

// Path materializes this binary's contract under its content-addressed cache key.
func Path() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate OpenAPI cache: %w", err)
	}
	return materialize(dir, OpenAPI)
}

func materialize(cacheDir string, data []byte) (string, error) {
	digest := sha256.Sum256(data)
	path := filepath.Join(cacheDir, "kapten", fmt.Sprintf("openapi-%x.yml", digest))
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, data) {
		return path, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read cached OpenAPI contract: %w", err)
	}
	if err := fileutil.WriteFile(path, data); err != nil {
		return "", fmt.Errorf("materialize embedded OpenAPI contract: %w", err)
	}
	return path, nil
}
