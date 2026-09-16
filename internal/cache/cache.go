// Package cache is a minimal content-addressed file cache for HTTP payloads.
//
// A nil *Cache is valid and behaves as a cache that never hits, which lets
// callers disable caching without branching.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Cache stores byte payloads on disk keyed by an arbitrary string.
type Cache struct {
	dir string
}

// New creates the directory if needed and returns a Cache rooted at dir.
func New(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("cache: create %s: %w", dir, err)
	}
	return &Cache{dir: dir}, nil
}

// Dir returns the cache root directory.
func (c *Cache) Dir() string {
	if c == nil {
		return ""
	}
	return c.dir
}

// Get returns the payload stored under key, if any.
func (c *Cache) Get(key string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	data, err := os.ReadFile(c.path(key))
	if err != nil {
		return nil, false
	}
	return data, true
}

// Put stores data under key. Writes are atomic via rename so concurrent
// readers never observe a partial file.
func (c *Cache) Put(key string, data []byte) error {
	if c == nil {
		return nil
	}
	final := c.path(key)
	tmp, err := os.CreateTemp(c.dir, "tmp-*")
	if err != nil {
		return fmt.Errorf("cache: temp file: %w", err)
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("cache: write: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("cache: rename: %w", err)
	}
	return nil
}

func (c *Cache) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:16])+".json")
}
