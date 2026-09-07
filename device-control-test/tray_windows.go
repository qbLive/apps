//go:build windows && xd_legacy_tray

package main

// Legacy compatibility placeholder.
// The active Windows system-tray implementation is integrated in main.go.
// Keeping this file excluded prevents duplicate Win32 constants, callbacks,
// tray icons, and startup behavior from being compiled into the agent.
