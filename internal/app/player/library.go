package player

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// A Library holds the announcement assets available to play into a call, cached
// in decoded form because the same asset plays on every single call and decoding
// it each time would be pointless work on the call setup path.
//
// Assets arrive over HTTP from our API, one per tenant, so the name is attacker
// controlled as far as this package is concerned and is validated against a
// strict pattern before it ever reaches the filesystem.
type Library struct {
	dir string

	mu    sync.RWMutex
	cache map[string][]float32
}

// safeName keeps asset names to characters that cannot escape the directory or
// mean anything special to a filesystem.
var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ErrNoLibrary is returned when no asset directory is configured, which is the
// default and means announcements are switched off.
var ErrNoLibrary = fmt.Errorf("no audio directory configured")

func NewLibrary(dir string) *Library {
	return &Library{dir: dir, cache: map[string][]float32{}}
}

// Enabled reports whether an asset directory was configured.
func (l *Library) Enabled() bool { return l != nil && l.dir != "" }

func (l *Library) path(name string) (string, error) {
	if !l.Enabled() {
		return "", ErrNoLibrary
	}
	if !safeName.MatchString(name) {
		return "", fmt.Errorf("invalid asset name %q", name)
	}
	if filepath.Ext(name) == "" {
		name += ".wav"
	}
	return filepath.Join(l.dir, name), nil
}

// Get returns the decoded samples for an asset, loading it on first use.
func (l *Library) Get(name string) ([]float32, error) {
	if !l.Enabled() {
		return nil, ErrNoLibrary
	}

	l.mu.RLock()
	cached, ok := l.cache[name]
	l.mu.RUnlock()
	if ok {
		return cached, nil
	}

	path, err := l.path(name)
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	pcm, err := decodeWAV(raw)
	if err != nil {
		return nil, fmt.Errorf("asset %s: %w", name, err)
	}

	l.mu.Lock()
	l.cache[name] = pcm
	l.mu.Unlock()

	return pcm, nil
}

// Store validates and writes an asset, replacing whatever was cached under that
// name. Our API pushes the asset here when a tenant uploads their announcement,
// so the engine never needs storage credentials of its own.
//
// The write goes to a temporary file and is renamed into place, so a call that
// loads the asset while an upload is in flight never sees a half-written file.
func (l *Library) Store(name string, raw []byte) (int64, error) {
	if !l.Enabled() {
		return 0, ErrNoLibrary
	}

	path, err := l.path(name)
	if err != nil {
		return 0, err
	}

	pcm, err := decodeWAV(raw)
	if err != nil {
		return 0, err
	}

	if err := os.MkdirAll(l.dir, 0o750); err != nil {
		return 0, err
	}

	tmp, err := os.CreateTemp(l.dir, ".upload-*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return 0, err
	}

	l.mu.Lock()
	l.cache[name] = pcm
	l.mu.Unlock()

	return durationMs(len(pcm)), nil
}

// Delete removes an asset and forgets it.
func (l *Library) Delete(name string) error {
	if !l.Enabled() {
		return ErrNoLibrary
	}

	path, err := l.path(name)
	if err != nil {
		return err
	}

	l.mu.Lock()
	delete(l.cache, name)
	l.mu.Unlock()

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
