package main

import (
	"log/slog"

	"wacalls/internal/app/config"
	"wacalls/internal/voip/call"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"google.golang.org/protobuf/proto"
)

// Fork additions that have to happen at boot, before anything else exists.
// Both targets are process-wide variables, so they are set once here rather
// than threaded through every constructor.

// applyDeviceProps names the paired device in the customer's linked-devices list.
//
// Upstream leaves store.DeviceProps alone, so a linked number shows up on the
// customer's phone as "whatsmeow". Two things worth knowing about this, both of
// them limitations rather than bugs:
//
//   - The name is process-wide, so every session on one instance shows the same
//     name. A per-customer name means a dedicated instance.
//   - The name is recorded when the device is linked, so an already-paired
//     number keeps whatever it was paired with until it is linked again.
//
// Os is the text on screen. PlatformType only picks the icon.
func applyDeviceProps(cfg config.Config) {
	store.DeviceProps.Os = proto.String(cfg.DeviceName)

	var platform waCompanionReg.DeviceProps_PlatformType
	switch cfg.DevicePlatform {
	case "firefox":
		platform = waCompanionReg.DeviceProps_FIREFOX
	case "desktop":
		platform = waCompanionReg.DeviceProps_DESKTOP
	case "safari":
		platform = waCompanionReg.DeviceProps_SAFARI
	case "edge":
		platform = waCompanionReg.DeviceProps_EDGE
	default:
		platform = waCompanionReg.DeviceProps_CHROME
	}
	store.DeviceProps.PlatformType = platform.Enum()

	slog.Debug("paired device identity set", "name", cfg.DeviceName, "platform", cfg.DevicePlatform)
}

// applyRingTimeout overrides how long an unanswered call rings. Campaigns want
// different ring times, and upstream hard-codes 60 seconds.
//
// DefaultTimeouts is a package variable copied into each CallManager at
// construction, so assigning to it here reaches every later call and no call
// that already exists. At boot there are none, which is why this belongs here.
func applyRingTimeout(cfg config.Config) {
	if cfg.RingTimeout <= 0 {
		return
	}
	call.DefaultTimeouts.Ring = cfg.RingTimeout
	slog.Info("ring timeout overridden", "ring", cfg.RingTimeout)
}
