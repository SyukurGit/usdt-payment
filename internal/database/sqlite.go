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

	if err := SyncAppsBetweenDBs(); err != nil {
		log.Printf("[DB Sync Warning] %v", err)
	}

	ensureDefaultDemoApp()

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

func CreateAppDual(name, webhookURL string) (*App, error) {
	appID := "app_" + RandomAlphaNumeric(12)
	apiKey := "ak_" + RandomHex(16) // 32 hex chars
	createdAt := time.Now().Format("2006-01-02 15:04:05")

	app := &App{
		ID:         appID,
		Name:       name,
		APIKey:     apiKey,
		WebhookURL: webhookURL,
		CreatedAt:  createdAt,
	}

	query := `INSERT INTO apps (id, name, api_key, webhook_url, created_at) VALUES (?, ?, ?, ?, ?)`

	if _, err := LiveDB.Exec(query, app.ID, app.Name, app.APIKey, app.WebhookURL, app.CreatedAt); err != nil {
		return nil, fmt.Errorf("failed to insert app to LiveDB: %w", err)
	}

	if _, err := SandboxDB.Exec(query, app.ID, app.Name, app.APIKey, app.WebhookURL, app.CreatedAt); err != nil {
		return nil, fmt.Errorf("failed to insert app to SandboxDB: %w", err)
	}

	return app, nil
}

func DeleteAppDual(appID string) error {
	query := `DELETE FROM apps WHERE id = ?`
	if _, err := LiveDB.Exec(query, appID); err != nil {
		return fmt.Errorf("failed to delete from LiveDB: %w", err)
	}
	if _, err := SandboxDB.Exec(query, appID); err != nil {
		return fmt.Errorf("failed to delete from SandboxDB: %w", err)
	}
	return nil
}

func SyncAppsBetweenDBs() error {
	if LiveDB == nil || SandboxDB == nil {
		return nil
	}

	rowsLive, err := LiveDB.Query("SELECT id, name, api_key, webhook_url, created_at FROM apps")
	if err == nil {
		defer rowsLive.Close()
		for rowsLive.Next() {
			var a App
			if err := rowsLive.Scan(&a.ID, &a.Name, &a.APIKey, &a.WebhookURL, &a.CreatedAt); err == nil {
				_, _ = SandboxDB.Exec(`
					INSERT OR IGNORE INTO apps (id, name, api_key, webhook_url, created_at)
					VALUES (?, ?, ?, ?, ?)
				`, a.ID, a.Name, a.APIKey, a.WebhookURL, a.CreatedAt)
			}
		}
	}

	rowsSandbox, err := SandboxDB.Query("SELECT id, name, api_key, webhook_url, created_at FROM apps")
	if err == nil {
		defer rowsSandbox.Close()
		for rowsSandbox.Next() {
			var a App
			if err := rowsSandbox.Scan(&a.ID, &a.Name, &a.APIKey, &a.WebhookURL, &a.CreatedAt); err == nil {
				_, _ = LiveDB.Exec(`
					INSERT OR IGNORE INTO apps (id, name, api_key, webhook_url, created_at)
					VALUES (?, ?, ?, ?, ?)
				`, a.ID, a.Name, a.APIKey, a.WebhookURL, a.CreatedAt)
			}
		}
	}

	return nil
}

func ensureDefaultDemoApp() {
	var count int
	_ = LiveDB.QueryRow("SELECT COUNT(*) FROM apps").Scan(&count)
	if count == 0 {
		_, _ = CreateAppDual("Demo Store (Sandbox & Live)", "http://localhost:3400/api/sandbox/demo-webhook")
		log.Println("[Database] Inisialisasi default Demo Store berhasil dibuat.")
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
