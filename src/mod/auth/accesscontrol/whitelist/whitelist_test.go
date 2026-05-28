package whitelist

import (
	"os"
	"path/filepath"
	"testing"

	"imuslab.com/arozos/mod/database"
)

func newTestDB(t *testing.T) *database.Database {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "testdb.db")
	f, err := os.Create(dbPath)
	if err != nil {
		t.Fatalf("Failed to create db file: %v", err)
	}
	f.Close()

	db, err := database.NewDatabase(dbPath, false)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestWhiteList_SetWhitelistEnabled(t *testing.T) {
	sysdb := newTestDB(t)
	wl := NewWhitelistManager(sysdb)

	wl.SetWhitelistEnabled(true)
	if !wl.Enabled {
		t.Error("Expected whitelist to be enabled")
	}

	wl.SetWhitelistEnabled(false)
	if wl.Enabled {
		t.Error("Expected whitelist to be disabled")
	}
}

func TestWhiteList_IsWhitelisted(t *testing.T) {
	sysdb := newTestDB(t)
	wl := NewWhitelistManager(sysdb)

	// IP is whitelisted when whitelist is disabled
	if !wl.IsWhitelisted("192.168.1.1") {
		t.Error("Expected IP to be whitelisted when whitelist is disabled")
	}

	// Enable whitelist and whitelist specific IP
	wl.SetWhitelistEnabled(true)
	_ = wl.SetWhitelist("192.168.1.1")

	if !wl.IsWhitelisted("192.168.1.1") || wl.IsWhitelisted("192.168.2.1") {
		t.Error("Unexpected whitelisting behavior")
	}

	// Reserved IP addresses are always whitelisted
	if !wl.IsWhitelisted("127.0.0.1") || !wl.IsWhitelisted("localhost") {
		t.Error("Expected reserved IP addresses to be whitelisted")
	}
}

func TestWhiteList_ListWhitelistedIpRanges(t *testing.T) {
	sysdb := newTestDB(t)
	wl := NewWhitelistManager(sysdb)
	wl.SetWhitelistEnabled(true)

	// No whitelisted IP ranges (only the default localhost range)
	if len(wl.ListWhitelistedIpRanges()) != 1 {
		t.Error("Expected no whitelisted IP ranges")
	}

	// Add two IP ranges
	_ = wl.SetWhitelist("192.168.1.1")
	_ = wl.SetWhitelist("192.168.2.1-192.168.2.254")

	whitelistedRanges := wl.ListWhitelistedIpRanges()
	if len(whitelistedRanges) != 3 {
		t.Error("Expected 3 whitelisted IP ranges")
	}
}

func TestWhiteList_SetWhitelist_UnsetWhitelist(t *testing.T) {
	sysdb := newTestDB(t)
	wl := NewWhitelistManager(sysdb)
	wl.SetWhitelistEnabled(true)

	// Set whitelist for a specific IP
	err := wl.SetWhitelist("192.168.1.1")
	if err != nil || !wl.IsWhitelisted("192.168.1.1") {
		t.Error("Unexpected error or IP not whitelisted")
	}

	// Set whitelist for an IP range
	err = wl.SetWhitelist("192.168.2.1-192.168.2.254")
	if err != nil || !wl.IsWhitelisted("192.168.2.5") {
		t.Error("Unexpected error or IP range not whitelisted")
	}

	// Unset whitelist for a specific IP
	err = wl.UnsetWhitelist("192.168.1.1")
	if err != nil || wl.IsWhitelisted("192.168.1.1") {
		t.Error("Unexpected error or IP still whitelisted")
	}

	// Unset whitelist for an IP range
	err = wl.UnsetWhitelist("192.168.2.1-192.168.2.254")
	if err != nil || wl.IsWhitelisted("192.168.2.5") {
		t.Error("Unexpected error or IP range still whitelisted")
	}

	// Unset whitelist for an invalid IP range
	err = wl.UnsetWhitelist("invalid-ip-range")
	if err == nil {
		t.Error("Expected error for invalid IP range")
	}
}
