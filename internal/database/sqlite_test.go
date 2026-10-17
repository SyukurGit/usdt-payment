package database

import (
	"strings"
	"testing"
)

func TestInitDatabases(t *testing.T) {
	liveDB, sandboxDB, err := InitDatabases()
	if err != nil {
		t.Fatalf("InitDatabases failed: %v", err)
	}

	// 1. Cek semua keys di LiveDB harus diawali ak_live_
	rowsLive, err := liveDB.Query("SELECT id, name, api_key FROM apps")
	if err != nil {
		t.Fatalf("Query LiveDB failed: %v", err)
	}
	defer rowsLive.Close()

	liveCount := 0
	for rowsLive.Next() {
		var id, name, key string
		if err := rowsLive.Scan(&id, &name, &key); err != nil {
			t.Fatalf("Scan LiveDB failed: %v", err)
		}
		liveCount++
		if !strings.HasPrefix(key, "ak_live_") {
			t.Errorf("LiveDB key '%s' does not start with ak_live_ (app: %s, %s)", key, id, name)
		}
	}
	if liveCount == 0 {
		t.Errorf("LiveDB should have at least 1 app")
	}

	// 2. Cek semua keys di SandboxDB harus diawali ak_test_
	rowsSandbox, err := sandboxDB.Query("SELECT id, name, api_key FROM apps")
	if err != nil {
		t.Fatalf("Query SandboxDB failed: %v", err)
	}
	defer rowsSandbox.Close()

	sandboxCount := 0
	for rowsSandbox.Next() {
		var id, name, key string
		if err := rowsSandbox.Scan(&id, &name, &key); err != nil {
			t.Fatalf("Scan SandboxDB failed: %v", err)
		}
		sandboxCount++
		if !strings.HasPrefix(key, "ak_test_") {
			t.Errorf("SandboxDB key '%s' does not start with ak_test_ (app: %s, %s)", key, id, name)
		}
	}
	if sandboxCount == 0 {
		t.Errorf("SandboxDB should have at least 1 app")
	}

	// 3. Test CreateAppPair
	liveApp, sandboxApp, err := CreateAppPair("Test Toko Baru", "https://toko.com/webhook", "")
	if err != nil {
		t.Fatalf("CreateAppPair failed: %v", err)
	}
	if !strings.HasPrefix(liveApp.APIKey, "ak_live_") {
		t.Errorf("CreateAppPair live key invalid: %s", liveApp.APIKey)
	}
	if !strings.HasPrefix(sandboxApp.APIKey, "ak_test_") {
		t.Errorf("CreateAppPair sandbox key invalid: %s", sandboxApp.APIKey)
	}
	if liveApp.APIKey == sandboxApp.APIKey {
		t.Errorf("Live and sandbox keys MUST NOT BE EQUAL!")
	}

	// Clean up test app
	_ = DeleteAppPair(liveApp.ID)
}
