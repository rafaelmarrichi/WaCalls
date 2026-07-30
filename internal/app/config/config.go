package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr           string
	DBPath         string
	StaticDir      string
	Version        string
	Debug          bool
	MaxCalls       int
	DatabaseURL    string
	APIToken       string
	AdminUser      string
	AdminPassword  string
	CORSOrigins    string
	RateLimit      float64
	WebRTCUDPPort  int
	PublicIPs      []string
	WebhookURL     string
	WebhookSecret  string
	TrustedProxies string
	DiagDir        string
	STUNServers    []string

	// Fork additions. Every one of these is inert when unset, so a binary built
	// from this fork behaves like upstream until it is configured. Helpers and
	// defaults live in malamute.go.
	RecordDir      string        // empty disables recording entirely
	RecordMaxBytes int64         // per-file ceiling
	AudioDir       string        // empty disables announcements entirely
	RingTimeout    time.Duration // zero keeps the upstream default
	DeviceName     string        // name shown in the customer's linked-devices list
	DevicePlatform string        // icon only, no effect on the name
}

func LoadConfig(addr, dbPath, staticDir string, debug bool, maxCalls int) Config {
	return Config{
		Addr:           addr,
		DBPath:         dbPath,
		StaticDir:      staticDir,
		Debug:          debug,
		MaxCalls:       maxCalls,
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		APIToken:       os.Getenv("WACALLS_API_TOKEN"),
		AdminUser:      strings.TrimSpace(os.Getenv("WACALLS_ADMIN_USER")),
		AdminPassword:  os.Getenv("WACALLS_ADMIN_PASSWORD"),
		CORSOrigins:    os.Getenv("WACALLS_CORS_ORIGINS"),
		RateLimit:      parseRateLimit(os.Getenv("WACALLS_RATE_LIMIT")),
		WebRTCUDPPort:  parseUDPPort(os.Getenv("WACALLS_WEBRTC_UDP_PORT")),
		PublicIPs:      parsePublicIPs(os.Getenv("WACALLS_PUBLIC_IP")),
		WebhookURL:     strings.TrimSpace(os.Getenv("WACALLS_WEBHOOK_URL")),
		WebhookSecret:  os.Getenv("WACALLS_WEBHOOK_SECRET"),
		TrustedProxies: os.Getenv("WACALLS_TRUSTED_PROXIES"),
		DiagDir:        strings.TrimSpace(os.Getenv("WACALLS_DIAG_DIR")),
		STUNServers:    parseSTUNServers(os.Getenv("WACALLS_STUN_SERVER")),

		RecordDir:      strings.TrimSpace(os.Getenv("WACALLS_RECORD_DIR")),
		RecordMaxBytes: parseRecordMaxBytes(os.Getenv("WACALLS_RECORD_MAX_MB")),
		AudioDir:       strings.TrimSpace(os.Getenv("WACALLS_AUDIO_DIR")),
		RingTimeout:    parseRingTimeout(os.Getenv("WACALLS_RING_TIMEOUT_SEC")),
		DeviceName:     parseDeviceName(os.Getenv("WACALLS_DEVICE_NAME")),
		DevicePlatform: parseDevicePlatform(os.Getenv("WACALLS_DEVICE_PLATFORM")),
	}
}

func Validate(cfg Config) error {
	if cfg.WebhookURL != "" && cfg.WebhookSecret == "" {
		return errors.New("WACALLS_WEBHOOK_URL requires WACALLS_WEBHOOK_SECRET")
	}
	if (cfg.AdminUser == "") != (cfg.AdminPassword == "") {
		return errors.New("WACALLS_ADMIN_USER and WACALLS_ADMIN_PASSWORD must be set together")
	}
	return nil
}

const defaultRateLimitRPS = 20

func parseRateLimit(raw string) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultRateLimitRPS
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return defaultRateLimitRPS
	}
	if n < 0 {
		return 0
	}
	return n
}

func parseUDPPort(raw string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(raw))
	return n
}

func parsePublicIPs(raw string) []string {
	var out []string
	for p := range strings.SplitSeq(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var defaultSTUNServers = []string{"stun.l.google.com:19302", "stun.cloudflare.com:3478"}

func parseSTUNServers(raw string) []string {
	out := parsePublicIPs(raw)
	if len(out) == 0 {
		return defaultSTUNServers
	}
	return out
}
