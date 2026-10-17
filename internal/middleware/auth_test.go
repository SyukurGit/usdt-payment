package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"paymentg-bsc/internal/database"
)

func TestRequireAPIKeyPrefixProtection(t *testing.T) {
	gin.SetMode(gin.TestMode)

	liveDB, sandboxDB, err := database.InitDatabases()
	if err != nil {
		t.Fatalf("InitDatabases failed: %v", err)
	}

	// Buat router Live
	rLive := gin.New()
	rLive.Use(RequireAPIKey(liveDB, false))
	rLive.GET("/test-live", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Buat router Sandbox
	rSandbox := gin.New()
	rSandbox.Use(RequireAPIKey(sandboxDB, true))
	rSandbox.GET("/test-sandbox", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 1. Kirim ak_test_ ke Live endpoint -> Harus 401
	req1, _ := http.NewRequest("GET", "/test-live", nil)
	req1.Header.Set("X-API-Key", "ak_test_1234567890abcdef1234567890abcdef")
	w1 := httptest.NewRecorder()
	rLive.ServeHTTP(w1, req1)
	if w1.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 when sending ak_test_ to Live endpoint, got %d", w1.Code)
	}

	// 2. Kirim ak_live_ ke Sandbox endpoint -> Harus 401
	req2, _ := http.NewRequest("GET", "/test-sandbox", nil)
	req2.Header.Set("X-API-Key", "ak_live_1234567890abcdef1234567890abcdef")
	w2 := httptest.NewRecorder()
	rSandbox.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 when sending ak_live_ to Sandbox endpoint, got %d", w2.Code)
	}

	// 3. Ambil key demo live yang valid
	var validLiveKey string
	_ = liveDB.QueryRow("SELECT api_key FROM apps WHERE api_key LIKE 'ak_live_%' LIMIT 1").Scan(&validLiveKey)
	if validLiveKey != "" {
		req3, _ := http.NewRequest("GET", "/test-live", nil)
		req3.Header.Set("X-API-Key", validLiveKey)
		w3 := httptest.NewRecorder()
		rLive.ServeHTTP(w3, req3)
		if w3.Code != http.StatusOK {
			t.Errorf("Expected 200 for valid live key on live endpoint, got %d: %s", w3.Code, w3.Body.String())
		}
	}

	// 4. Ambil key demo sandbox yang valid
	var validSandboxKey string
	_ = sandboxDB.QueryRow("SELECT api_key FROM apps WHERE api_key LIKE 'ak_test_%' LIMIT 1").Scan(&validSandboxKey)
	if validSandboxKey != "" {
		req4, _ := http.NewRequest("GET", "/test-sandbox", nil)
		req4.Header.Set("X-API-Key", validSandboxKey)
		w4 := httptest.NewRecorder()
		rSandbox.ServeHTTP(w4, req4)
		if w4.Code != http.StatusOK {
			t.Errorf("Expected 200 for valid sandbox key on sandbox endpoint, got %d: %s", w4.Code, w4.Body.String())
		}
	}
}
