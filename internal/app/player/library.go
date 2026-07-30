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
//
// The cache is keyed by the canonical name, never by the name as it arrived.
// `path` appends `.wav` when the caller omits the extension, so "aviso" and
// "aviso.wav" are the same file: keying by the raw name gave them separate cache
// entries, and replacing one left the other serving the old audio forever. For
// the legal announcement that means playing a superseded notice with no error
// anywhere, and a deleted asset that keeps playing.
//
// It is also bounded. Every distinct name that is ever played becomes resident
// decoded audio, and an API that versions assets per tenant would grow the heap
// without limit in a process that runs for months.
type Library struct {
	dir string

	mu    sync.RWMutex
	cache map[string][]float32
	// Ordem de chegada das chaves, para descartar a mais antiga quando lotar.
	ordem []string
}

// Extensão implícita quando o chamador não informa uma.
const extensaoPadrao = ".wav"

// Teto de assets decodificados em memória.
//
// Um aviso de seis segundos em 16 kHz ocupa cerca de 380 KB decodificado, então
// 64 entradas são uns 24 MB no pior caso. É folga larga sobre o uso real, que é
// um aviso por cliente numa instância compartilhada por poucos.
const tetoDoCache = 64

// canonico devolve o nome sob o qual o asset é cacheado, que é o mesmo que
// compõe o caminho no disco.
func canonico(name string) string {
	if filepath.Ext(name) == "" {
		return name + extensaoPadrao
	}
	return name
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
	return filepath.Join(l.dir, canonico(name)), nil
}

// Get returns the decoded samples for an asset, loading it on first use.
func (l *Library) Get(name string) ([]float32, error) {
	if !l.Enabled() {
		return nil, ErrNoLibrary
	}

	l.mu.RLock()
	cached, ok := l.cache[canonico(name)]
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

	l.guardar(canonico(name), pcm)

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

	l.guardar(canonico(name), pcm)

	return durationMs(len(pcm)), nil
}

// guardar coloca o asset no cache, descartando o mais antigo quando lota.
func (l *Library) guardar(chave string, pcm []float32) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, jaTinha := l.cache[chave]; !jaTinha {
		l.ordem = append(l.ordem, chave)
	}
	l.cache[chave] = pcm

	// Descarta os mais antigos, e não os menos usados: o padrão de uso aqui é um
	// asset por cliente tocando o dia inteiro, então idade e frequência dizem a
	// mesma coisa, e o mais antigo é quase sempre um asset substituído. Sair do
	// cache não perde nada: a próxima reprodução lê do disco de novo.
	for len(l.ordem) > tetoDoCache {
		velho := l.ordem[0]
		l.ordem = l.ordem[1:]
		delete(l.cache, velho)
	}
}

// esquecer tira o asset do cache.
func (l *Library) esquecer(chave string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.cache, chave)
	for i, item := range l.ordem {
		if item == chave {
			l.ordem = append(l.ordem[:i], l.ordem[i+1:]...)
			break
		}
	}
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

	l.esquecer(canonico(name))

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
