package config

import (
	"testing"
	"time"
)

// Every fork setting has to be inert when unset, so a binary built from this fork
// behaves like upstream until it is deliberately configured. These tests exist to
// keep that property from eroding.

func TestForkSettingsAreOffByDefault(t *testing.T) {
	t.Setenv("WACALLS_RECORD_DIR", "")
	t.Setenv("WACALLS_AUDIO_DIR", "")
	t.Setenv("WACALLS_RING_TIMEOUT_SEC", "")
	t.Setenv("WACALLS_RECORD_MAX_MB", "")
	t.Setenv("WACALLS_DEVICE_NAME", "")
	t.Setenv("WACALLS_DEVICE_PLATFORM", "")

	cfg := LoadConfig(":8080", "db", "", false, 8)

	if cfg.RecordDir != "" {
		t.Errorf("recording is on by default: %q", cfg.RecordDir)
	}
	if cfg.AudioDir != "" {
		t.Errorf("announcements are on by default: %q", cfg.AudioDir)
	}
	if cfg.RingTimeout != 0 {
		t.Errorf("ring timeout overrides the upstream default: %v", cfg.RingTimeout)
	}

	// These two always have a value, because there is no sensible "off": the
	// device is named something either way, and upstream names it after the
	// library.
	if cfg.DeviceName != DefaultDeviceName {
		t.Errorf("device name: want %q, got %q", DefaultDeviceName, cfg.DeviceName)
	}
	if cfg.DevicePlatform != DefaultDevicePlatform {
		t.Errorf("device platform: want %q, got %q", DefaultDevicePlatform, cfg.DevicePlatform)
	}
	if want := int64(DefaultRecordMaxMB) << 20; cfg.RecordMaxBytes != want {
		t.Errorf("record cap: want %d, got %d", want, cfg.RecordMaxBytes)
	}
}

func TestForkSettingsFromEnv(t *testing.T) {
	t.Setenv("WACALLS_RECORD_DIR", "  /data/recordings  ")
	t.Setenv("WACALLS_AUDIO_DIR", " /data/audio ")
	t.Setenv("WACALLS_RECORD_MAX_MB", "50")
	t.Setenv("WACALLS_RING_TIMEOUT_SEC", "35")
	t.Setenv("WACALLS_DEVICE_NAME", "  Discador do Cliente  ")
	t.Setenv("WACALLS_DEVICE_PLATFORM", "FireFox")

	cfg := LoadConfig(":8080", "db", "", false, 8)

	if cfg.RecordDir != "/data/recordings" {
		t.Errorf("record dir: got %q", cfg.RecordDir)
	}
	if cfg.AudioDir != "/data/audio" {
		t.Errorf("audio dir: got %q", cfg.AudioDir)
	}
	if want := int64(50) << 20; cfg.RecordMaxBytes != want {
		t.Errorf("record cap: want %d, got %d", want, cfg.RecordMaxBytes)
	}
	if cfg.RingTimeout != 35*time.Second {
		t.Errorf("ring timeout: got %v", cfg.RingTimeout)
	}
	if cfg.DeviceName != "Discador do Cliente" {
		t.Errorf("device name: got %q", cfg.DeviceName)
	}
	if cfg.DevicePlatform != "firefox" {
		t.Errorf("device platform: got %q", cfg.DevicePlatform)
	}
}

// A bad value must not silently become something surprising, because these two
// change how calls behave and how much disk a call can consume.
func TestForkSettingsIgnoreGarbage(t *testing.T) {
	for _, raw := range []string{"abc", "-1", "0", "", "  ", "1.5"} {
		if got := parseRingTimeout(raw); got != 0 {
			t.Errorf("ring timeout from %q: want 0 (keep upstream), got %v", raw, got)
		}
		if got := parseRecordMaxBytes(raw); got != int64(DefaultRecordMaxMB)<<20 {
			t.Errorf("record cap from %q: want the default, got %d", raw, got)
		}
	}
}

// An unknown platform picks the default icon rather than failing to boot: the
// icon is cosmetic and is not worth refusing to start over.
func TestDevicePlatformFallsBack(t *testing.T) {
	for _, raw := range []string{"netscape", "", "  ", "opera"} {
		if got := parseDevicePlatform(raw); got != DefaultDevicePlatform {
			t.Errorf("platform from %q: want %q, got %q", raw, DefaultDevicePlatform, got)
		}
	}
	for _, raw := range []string{"chrome", "CHROME", " Desktop ", "safari", "edge"} {
		if got := parseDevicePlatform(raw); got == "" {
			t.Errorf("platform from %q was rejected", raw)
		}
	}
}
