package config

import (
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

func parseRecordMaxBytes(raw string) int64 {
	mb := DefaultRecordMaxMB
	if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 {
		mb = n
	}
	return int64(mb) << 20
}

// parseRingTimeout reads how long an unanswered call may ring. Zero means keep
// whatever upstream decided, so an unset or unparsable value changes nothing.
func parseRingTimeout(raw string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
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
	switch p := strings.ToLower(strings.TrimSpace(raw)); p {
	case "chrome", "firefox", "desktop", "safari", "edge":
		return p
	default:
		return DefaultDevicePlatform
	}
}
