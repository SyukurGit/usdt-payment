package database

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type App struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	APIKey     string `json:"api_key"`
	WebhookURL string `json:"webhook_url"`
	CreatedAt  string `json:"created_at"`
}

type Order struct {
	ID                 string  `json:"id"`
	AppID              string  `json:"app_id"`
	ReferenceID        string  `json:"reference_id"`
	Network            string  `json:"network"`
	Currency           string  `json:"currency"`
	WalletAddress      string  `json:"wallet_address"`
	OriginalAmount     float64 `json:"original_amount"`
	OriginalAmountMilli int64  `json:"original_amount_milli"`
	UniqueCode         int     `json:"unique_code"`
	TotalAmount        float64 `json:"total_amount"`
	TotalAmountStr     string  `json:"total_amount_str"`
	TotalAmountMilli   int64   `json:"total_amount_milli"`
	Status             string  `json:"status"` // PENDING, PAID, EXPIRED, CANCELLED
	TxID               *string `json:"tx_id"`
	TxHash             string  `json:"tx_hash"`
	FromAddress        string  `json:"from_address"`
	BlockNumber        uint64  `json:"block_number"`
	QRPath             string  `json:"qr_path"`
	CreatedAt          int64   `json:"created_at"`
	ExpiresAt          int64   `json:"expires_at"`
	CooldownUntil      int64   `json:"cooldown_until"`
	PaidAt             int64   `json:"paid_at"`
	WebhookDelivered   int     `json:"webhook_delivered"`
	WebhookAttempts    int     `json:"webhook_attempts"`
	NextWebhookRetryAt int64   `json:"next_webhook_retry_at"`
}

type UnmatchedTx struct {
	TxID            string `json:"tx_id"`
	TxHash          string `json:"tx_hash"`
	FromAddress     string `json:"from_address"`
	Currency        string `json:"currency"`
	AmountStr       string `json:"amount_str"`
	AmountMilli     int64  `json:"amount_milli"`
	RawWei          string `json:"raw_wei"`
	BlockNumber     uint64 `json:"block_number"`
	TxTime          int64  `json:"tx_time"`
	ResolvedOrderID string `json:"resolved_order_id"`
	RecordedAt      string `json:"recorded_at"`
}

var (
	LiveDB    *sql.DB
	SandboxDB *sql.DB
)

func InitDatabases() (*sql.DB, *sql.DB, error) {
	livePath := filepath.Join("data", "payment.db")
	sandboxPath := filepath.Join("data", "sandbox", "payment.db")

	if err := os.MkdirAll(filepath.Dir(livePath), 0755); err != nil {
		return nil, nil, fmt.Errorf("failed to create live db dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(sandboxPath), 0755); err != nil {
		return nil, nil, fmt.Errorf("failed to create sandbox db dir: %w", err)
	}

	liveConn, err := openSQLite(livePath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open LiveDB: %w", err)
	}

	sandboxConn, err := openSQLite(sandboxPath)
	if err != nil {
		liveConn.Close()
		return nil, nil, fmt.Errorf("failed to open SandboxDB: %w", err)
	}

	if err := migrateSchema(liveConn); err != nil {
		liveConn.Close()
		sandboxConn.Close()
		return nil, nil, fmt.Errorf("failed to migrate LiveDB: %w", err)
	}

	if err := migrateSchema(sandboxConn); err != nil {
		liveConn.Close()
		sandboxConn.Close()
		return nil, nil, fmt.Errorf("failed to migrate SandboxDB: %w", err)
	}

	LiveDB = liveConn
	SandboxDB = sandboxConn

	migrateLegacyKeys(LiveDB, SandboxDB)
	ensureDefaultDemoApps()

	return LiveDB, SandboxDB, nil
}

func openSQLite(dbPath string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrateSchema(db *sql.DB) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS system_config (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS apps (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			api_key TEXT NOT NULL UNIQUE,
			webhook_url TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS orders (
			id TEXT PRIMARY KEY,
			app_id TEXT NOT NULL,
			reference_id TEXT NOT NULL,
			network TEXT NOT NULL DEFAULT 'BSC',
			currency TEXT NOT NULL DEFAULT 'USDT',
			wallet_address TEXT NOT NULL,
			original_amount REAL NOT NULL,
			original_amount_milli INTEGER NOT NULL,
			unique_code INTEGER NOT NULL,
			total_amount REAL NOT NULL,
			total_amount_str TEXT NOT NULL,
			total_amount_milli INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'PENDING',
			tx_id TEXT UNIQUE,
			tx_hash TEXT DEFAULT '',
			from_address TEXT DEFAULT '',
			block_number INTEGER DEFAULT 0,
			qr_path TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			cooldown_until INTEGER NOT NULL,
			paid_at INTEGER DEFAULT 0,
			webhook_delivered INTEGER DEFAULT 0,
			webhook_attempts INTEGER DEFAULT 0,
			next_webhook_retry_at INTEGER DEFAULT 0,
			FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_orders_matching 
		ON orders(currency, total_amount_milli, status, cooldown_until);`,
		`CREATE INDEX IF NOT EXISTS idx_orders_status_created 
		ON orders(status, created_at, expires_at);`,
		`CREATE TABLE IF NOT EXISTS unmatched_transactions (
			tx_id TEXT PRIMARY KEY,
			tx_hash TEXT NOT NULL,
			from_address TEXT NOT NULL,
			currency TEXT NOT NULL DEFAULT 'USDT',
			amount_str TEXT NOT NULL,
			amount_milli INTEGER NOT NULL,
			raw_wei TEXT NOT NULL,
			block_number INTEGER NOT NULL,
			tx_time INTEGER NOT NULL,
			resolved_order_id TEXT DEFAULT '',
			recorded_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
	}

	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("migration query failed: %w (query: %s)", err, q)
		}
	}
	return nil
}

func GetSystemConfig(db *sql.DB, key string) (string, error) {
	var val string
	err := db.QueryRow("SELECT value FROM system_config WHERE key = ?", key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

func SetSystemConfig(db *sql.DB, key, value string) error {
	_, err := db.Exec(`
		INSERT INTO system_config (key, value, updated_at) 
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP
	`, key, value)
	return err
}

// CreateAppPair membuat 2 entitas toko terpisah:
// 1. Toko Live dengan API Key unik ber-prefix ak_live_ disimpan ke LiveDB.
// 2. Toko Sandbox dengan API Key unik ber-prefix ak_test_ disimpan ke SandboxDB.
// Keduanya memiliki rahasia kunci yang 100% berbeda untuk menjamin isolasi total.
func CreateAppPair(name, liveWebhookURL, sandboxWebhookURL string) (*App, *App, error) {
	if sandboxWebhookURL == "" {
		sandboxWebhookURL = liveWebhookURL
	}

	createdAt := time.Now().Format("2006-01-02 15:04:05")

	liveApp := &App{
		ID:         "app_live_" + RandomAlphaNumeric(10),
		Name:       name,
		APIKey:     "ak_live_" + RandomHex(16),
		WebhookURL: liveWebhookURL,
		CreatedAt:  createdAt,
	}

	sandboxApp := &App{
		ID:         "app_test_" + RandomAlphaNumeric(10),
		Name:       name + " (Sandbox)",
		APIKey:     "ak_test_" + RandomHex(16),
		WebhookURL: sandboxWebhookURL,
		CreatedAt:  createdAt,
	}

	query := `INSERT INTO apps (id, name, api_key, webhook_url, created_at) VALUES (?, ?, ?, ?, ?)`

	if _, err := LiveDB.Exec(query, liveApp.ID, liveApp.Name, liveApp.APIKey, liveApp.WebhookURL, liveApp.CreatedAt); err != nil {
		return nil, nil, fmt.Errorf("gagal membuat app di LiveDB: %w", err)
	}

	if _, err := SandboxDB.Exec(query, sandboxApp.ID, sandboxApp.Name, sandboxApp.APIKey, sandboxApp.WebhookURL, sandboxApp.CreatedAt); err != nil {
		// Rollback dari LiveDB jika sandbox gagal
		_, _ = LiveDB.Exec("DELETE FROM apps WHERE id = ?", liveApp.ID)
		return nil, nil, fmt.Errorf("gagal membuat app di SandboxDB: %w", err)
	}

	return liveApp, sandboxApp, nil
}

func DeleteAppPair(appID string) error {
	var appName string
	_ = LiveDB.QueryRow("SELECT name FROM apps WHERE id = ?", appID).Scan(&appName)

	// Hapus dari LiveDB
	if _, err := LiveDB.Exec("DELETE FROM apps WHERE id = ?", appID); err != nil {
		return fmt.Errorf("gagal menghapus dari LiveDB: %w", err)
	}

	// Hapus juga pasangan di SandboxDB jika cocok dengan appID atau nama
	if appName != "" {
		_, _ = SandboxDB.Exec("DELETE FROM apps WHERE id = ? OR name = ? OR name = ?", appID, appName, appName+" (Sandbox)")
	} else {
		_, _ = SandboxDB.Exec("DELETE FROM apps WHERE id = ?", appID)
	}
	return nil
}

func migrateLegacyKeys(liveDB, sandboxDB *sql.DB) {
	if liveDB == nil || sandboxDB == nil {
		return
	}

	type appRow struct {
		id, key, name string
	}

	// 1. Migrasikan kunci di LiveDB agar selalu memiliki prefix ak_live_
	var liveApps []appRow
	rowsLive, err := liveDB.Query("SELECT id, api_key, name FROM apps")
	if err == nil {
		for rowsLive.Next() {
			var r appRow
			if err := rowsLive.Scan(&r.id, &r.key, &r.name); err == nil {
				liveApps = append(liveApps, r)
			}
		}
		rowsLive.Close()
	}

	for _, a := range liveApps {
		newName := a.name
		if strings.Contains(a.name, "Demo Store") {
			newName = "Demo Store (Live)"
		}
		if !strings.HasPrefix(a.key, "ak_live_") {
			newKey := "ak_live_" + RandomHex(16)
			_, _ = liveDB.Exec("UPDATE apps SET api_key = ?, name = ? WHERE id = ?", newKey, newName, a.id)
			log.Printf("[DB Migration] Diperbarui Live App %s ke kunci ak_live_", a.id)
		} else if newName != a.name {
			_, _ = liveDB.Exec("UPDATE apps SET name = ? WHERE id = ?", newName, a.id)
		}
	}

	// 2. Migrasikan kunci di SandboxDB agar selalu memiliki prefix ak_test_
	var sandboxApps []appRow
	rowsSandbox, err := sandboxDB.Query("SELECT id, api_key, name FROM apps")
	if err == nil {
		for rowsSandbox.Next() {
			var r appRow
			if err := rowsSandbox.Scan(&r.id, &r.key, &r.name); err == nil {
				sandboxApps = append(sandboxApps, r)
			}
		}
		rowsSandbox.Close()
	}

	for _, a := range sandboxApps {
		newName := a.name
		if strings.Contains(a.name, "Demo Store") {
			newName = "Demo Store (Sandbox)"
		}
		if !strings.HasPrefix(a.key, "ak_test_") {
			newKey := "ak_test_" + RandomHex(16)
			_, _ = sandboxDB.Exec("UPDATE apps SET api_key = ?, name = ? WHERE id = ?", newKey, newName, a.id)
			log.Printf("[DB Migration] Diperbarui Sandbox App %s ke kunci ak_test_", a.id)
		} else if newName != a.name {
			_, _ = sandboxDB.Exec("UPDATE apps SET name = ? WHERE id = ?", newName, a.id)
		}
	}

	// 3. Update port webhook lokal jika sebelumnya tercatat 3400
	_, _ = liveDB.Exec("UPDATE apps SET webhook_url = replace(webhook_url, ':3400', ':3900') WHERE webhook_url LIKE '%:3400%'")
	_, _ = sandboxDB.Exec("UPDATE apps SET webhook_url = replace(webhook_url, ':3400', ':3900') WHERE webhook_url LIKE '%:3400%'")
}

func ensureDefaultDemoApps() {
	var liveCount int
	_ = LiveDB.QueryRow("SELECT COUNT(*) FROM apps").Scan(&liveCount)
	if liveCount == 0 {
		liveKey := "ak_live_" + RandomHex(16)
		now := time.Now().Format("2006-01-02 15:04:05")
		_, _ = LiveDB.Exec(`
			INSERT INTO apps (id, name, api_key, webhook_url, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, "app_live_demo", "Demo Store (Live)", liveKey, "http://localhost:3900/api/sandbox/demo-webhook", now)
		log.Println("[Database] Inisialisasi default Demo Store (Live) berhasil dibuat.")
	}

	var sandboxCount int
	_ = SandboxDB.QueryRow("SELECT COUNT(*) FROM apps").Scan(&sandboxCount)
	if sandboxCount == 0 {
		testKey := "ak_test_" + RandomHex(16)
		now := time.Now().Format("2006-01-02 15:04:05")
		_, _ = SandboxDB.Exec(`
			INSERT INTO apps (id, name, api_key, webhook_url, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, "app_test_demo", "Demo Store (Sandbox)", testKey, "http://localhost:3900/api/sandbox/demo-webhook", now)
		log.Println("[Database] Inisialisasi default Demo Store (Sandbox) berhasil dibuat.")
	}
}

func RandomAlphaNumeric(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())[:length]
	}
	for i := range bytes {
		bytes[i] = charset[bytes[i]%byte(len(charset))]
	}
	return string(bytes)
}

func RandomHex(byteLen int) string {
	bytes := make([]byte, byteLen)
	_, _ = rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

func CleanAddress(addr string) string {
	return strings.ToLower(strings.TrimSpace(addr))
}
