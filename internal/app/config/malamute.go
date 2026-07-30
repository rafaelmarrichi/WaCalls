package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Configuration added by the fork, kept in its own file so reapplying the patch
// after an upstream snapshot only has to touch the struct and the LoadConfig
// literal in config.go.

const (
	// DefaultRecordMaxMB caps one recording file. At 64 KB/s of stereo 16 kHz
	// PCM this is a little under an hour, comfortably past any real call.
	DefaultRecordMaxMB = 200

	// DefaultDeviceName is what shows up in the customer's linked-devices list.
	//
	// Upstream never touches store.DeviceProps, so the entry reads "whatsmeow",
	// the library name. That looks broken and gives the tooling away. The choice
	// here is a name of our own with a browser icon rather than impersonating a
	// browser: a customer who does not recognise an entry revokes it, and
	// revoking disconnects the number and stops the campaign.
	DefaultDeviceName = "Malamute Discador"

	DefaultDevicePlatform = "chrome"
)

// MaxRecordMB is the ceiling on the configurable recording size.
//
// The WAV header stores the data length in a uint32, so a file over 4 GiB would
// have its size silently truncated by the wrap and come out corrupt. Today the
// four-hour call limit puts the real maximum near 920 MB, so this is
// unreachable; it exists so that raising the duration limit later cannot quietly
// produce unplayable recordings.
const MaxRecordMB = 4000

// Problemas de configuração encontrados na leitura do ambiente.
//
// Devolvidos em vez de logados aqui porque este pacote não tem logger. Quem
// carrega a configuração os escreve no boot: um valor que não parseia e cai no
// padrão em silêncio faz o operador acreditar que limitou a gravação ou o toque
// quando não limitou nada.
var avisosDeConfig []string

// AvisosDeConfig devolve o que foi ignorado na leitura do ambiente.
func AvisosDeConfig() []string { return avisosDeConfig }

func avisar(formato string, args ...any) {
	avisosDeConfig = append(avisosDeConfig, fmt.Sprintf(formato, args...))
}

func parseRecordMaxBytes(raw string) int64 {
	mb := DefaultRecordMaxMB
	texto := strings.TrimSpace(raw)

	if texto != "" {
		n, err := strconv.Atoi(texto)
		switch {
		case err != nil || n <= 0:
			avisar("WACALLS_RECORD_MAX_MB=%q não é um número de megabytes válido, usando %d", raw, DefaultRecordMaxMB)
		case n > MaxRecordMB:
			avisar("WACALLS_RECORD_MAX_MB=%d passa do teto de %d MB do formato WAV, usando o teto", n, MaxRecordMB)
			mb = MaxRecordMB
		default:
			mb = n
		}
	}

	return int64(mb) << 20
}

// parseRingTimeout reads how long an unanswered call may ring. Zero means keep
// whatever upstream decided, so an unset value changes nothing.
func parseRingTimeout(raw string) time.Duration {
	texto := strings.TrimSpace(raw)
	if texto == "" {
		return 0
	}

	n, err := strconv.Atoi(texto)
	if err != nil || n <= 0 {
		avisar("WACALLS_RING_TIMEOUT_SEC=%q não é um número de segundos válido, mantendo o padrão", raw)
		return 0
	}

	return time.Duration(n) * time.Second
}

func parseDeviceName(raw string) string {
	if name := strings.TrimSpace(raw); name != "" {
		return name
	}
	return DefaultDeviceName
}

func parseDevicePlatform(raw string) string {
	texto := strings.ToLower(strings.TrimSpace(raw))

	switch texto {
	case "chrome", "firefox", "desktop", "safari", "edge":
		return texto
	case "":
		return DefaultDevicePlatform
	default:
		avisar("WACALLS_DEVICE_PLATFORM=%q não é uma plataforma conhecida, usando %q", raw, DefaultDevicePlatform)
		return DefaultDevicePlatform
	}
}
