package middleware

import (
	"crypto/subtle"
	"database/sql"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"paymentg-bsc/internal/config"
	"paymentg-bsc/internal/database"
)

type bruteForceTracker struct {
	mu           sync.Mutex
	failedCount  int
	lockedUntil  time.Time
}

var adminPINGuard = &bruteForceTracker{}

func ValidateAdminPIN(inputPin string) (bool, string, int) {
	time.Sleep(300 * time.Millisecond) // Anti-brute-force delay 300ms

	adminPINGuard.mu.Lock()
	defer adminPINGuard.mu.Unlock()

	now := time.Now()
	if now.Before(adminPINGuard.lockedUntil) {
		remaining := int(adminPINGuard.lockedUntil.Sub(now).Seconds())
		return false, "Terlalu banyak percobaan salah. Akun terkunci sementara.", remaining
	}

	expectedPin := config.AppConfig.AdminPIN
	if subtle.ConstantTimeCompare([]byte(inputPin), []byte(expectedPin)) == 1 {
		adminPINGuard.failedCount = 0
		return true, "", 0
	}

	adminPINGuard.failedCount++
	if adminPINGuard.failedCount >= 5 {
		adminPINGuard.lockedUntil = now.Add(5 * time.Minute)
		adminPINGuard.failedCount = 0
		return false, "5x salah PIN. Akses dikunci selama 5 menit.", 300
	}

	remainingAttempts := 5 - adminPINGuard.failedCount
	return false, "PIN salah.", remainingAttempts
}

func RequireAdminKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := strings.TrimSpace(c.GetHeader("X-Admin-Key"))
		if key == "" {
			authHeader := strings.TrimSpace(c.GetHeader("Authorization"))
			if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
				key = strings.TrimSpace(authHeader[7:])
			}
		}

		expectedKey := config.AppConfig.AdminKey

		if key == "" || subtle.ConstantTimeCompare([]byte(key), []byte(expectedKey)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"error":   "Unauthorized: X-Admin-Key atau Bearer Token tidak valid atau belum diisi.",
			})
			return
		}
		c.Next()
	}
}

func RequireAPIKey(db *sql.DB, isSandbox bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey := strings.TrimSpace(c.GetHeader("X-API-Key"))
		if apiKey == "" {
			authHeader := strings.TrimSpace(c.GetHeader("Authorization"))
			if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
				apiKey = strings.TrimSpace(authHeader[7:])
			}
		}

		if apiKey == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"error":   "Unauthorized: Header X-API-Key atau Bearer Token wajib disertakan.",
			})
			return
		}

		// Enforce Cross-Environment Protection
		if !isSandbox && strings.HasPrefix(apiKey, "ak_test_") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"error":   "Unauthorized: Kunci Sandbox (ak_test_...) tidak dapat digunakan pada endpoint Live. Gunakan kunci ak_live_...",
			})
			return
		}

		if isSandbox && strings.HasPrefix(apiKey, "ak_live_") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"error":   "Unauthorized: Kunci Live (ak_live_...) tidak dapat digunakan pada endpoint Sandbox. Gunakan kunci ak_test_...",
			})
			return
		}

		var app database.App
		err := db.QueryRow("SELECT id, name, api_key, webhook_url, created_at FROM apps WHERE api_key = ?", apiKey).
			Scan(&app.ID, &app.Name, &app.APIKey, &app.WebhookURL, &app.CreatedAt)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"error":   "Unauthorized: API Key tidak valid.",
			})
			return
		}

		c.Set("app", app)
		c.Set("app_id", app.ID)
		c.Next()
	}
}
