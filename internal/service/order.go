package service

import (
	"database/sql"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	"paymentg-bsc/internal/config"
	"paymentg-bsc/internal/database"
)

type OrderService struct {
	mu         sync.Mutex
	qrService  *QRService
	webhookSvc *WebhookService
}

func NewOrderService(qrService *QRService, webhookSvc *WebhookService) *OrderService {
	return &OrderService{
		qrService:  qrService,
		webhookSvc: webhookSvc,
	}
}

type CreateOrderParams struct {
	AppID         string
	ReferenceID   string
	Amount        float64
	Currency      string
	WalletAddress string
	IsSandbox     bool
}

func (s *OrderService) CreateOrder(db *sql.DB, params CreateOrderParams) (*database.Order, error) {
	if params.Amount < 0.01 {
		return nil, fmt.Errorf("amount minimal adalah 0.01 USDT")
	}

	if params.Currency == "" {
		params.Currency = config.AppConfig.DefaultCurrency
	}
	if params.WalletAddress == "" {
		params.WalletAddress = config.AppConfig.GetReceiverWallet()
	}

	// Idempotensi: Cek apakah ada order dengan app_id + reference_id yang masih PENDING
	var existing database.Order
	var txID sql.NullString
	err := db.QueryRow(`
		SELECT id, app_id, reference_id, network, currency, wallet_address,
		       original_amount, original_amount_milli, unique_code, total_amount,
		       total_amount_str, total_amount_milli, status, tx_id, tx_hash,
		       from_address, block_number, qr_path, created_at, expires_at,
		       cooldown_until, paid_at, webhook_delivered, webhook_attempts
		FROM orders 
		WHERE app_id = ? AND reference_id = ? AND status = 'PENDING'
	`, params.AppID, params.ReferenceID).Scan(
		&existing.ID, &existing.AppID, &existing.ReferenceID, &existing.Network, &existing.Currency,
		&existing.WalletAddress, &existing.OriginalAmount, &existing.OriginalAmountMilli,
		&existing.UniqueCode, &existing.TotalAmount, &existing.TotalAmountStr, &existing.TotalAmountMilli,
		&existing.Status, &txID, &existing.TxHash, &existing.FromAddress, &existing.BlockNumber,
		&existing.QRPath, &existing.CreatedAt, &existing.ExpiresAt, &existing.CooldownUntil,
		&existing.PaidAt, &existing.WebhookDelivered, &existing.WebhookAttempts,
	)
	if err == nil {
		if txID.Valid {
			existing.TxID = &txID.String
		}
		// Pastikan file QR ada di disk
		_, _ = s.qrService.EnsureQRCode(existing.ID, existing.WalletAddress, params.IsSandbox)
		return &existing, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	origMilli := int64(math.Round(params.Amount * 1000.0))

	// Alokasi kode unik adaptif
	uniqueCode, err := s.allocateUniqueCode(db, params.Currency, origMilli, params.Amount)
	if err != nil {
		return nil, fmt.Errorf("gagal mengalokasikan kode unik: %w", err)
	}

	totalMilli := origMilli + int64(uniqueCode)
	totalAmount := float64(totalMilli) / 1000.0
	totalAmountStr := fmt.Sprintf("%.3f", totalAmount)

	orderID := "ord_" + database.RandomAlphaNumeric(12)
	nowMs := time.Now().UnixMilli()
	expiresAt := nowMs + (int64(config.AppConfig.DefaultExpiryMinutes) * 60 * 1000)
	cooldownUntil := expiresAt + (int64(config.AppConfig.CooldownMinutes) * 60 * 1000)

	qrPath, err := s.qrService.GenerateQRCode(orderID, params.WalletAddress, params.IsSandbox)
	if err != nil {
		return nil, fmt.Errorf("gagal generate QR code: %w", err)
	}

	order := &database.Order{
		ID:                  orderID,
		AppID:               params.AppID,
		ReferenceID:         params.ReferenceID,
		Network:             config.AppConfig.NetworkName,
		Currency:            params.Currency,
		WalletAddress:       params.WalletAddress,
		OriginalAmount:      params.Amount,
		OriginalAmountMilli: origMilli,
		UniqueCode:          uniqueCode,
		TotalAmount:         totalAmount,
		TotalAmountStr:      totalAmountStr,
		TotalAmountMilli:    totalMilli,
		Status:              "PENDING",
		QRPath:              qrPath,
		CreatedAt:           nowMs,
		ExpiresAt:           expiresAt,
		CooldownUntil:       cooldownUntil,
		PaidAt:              0,
		WebhookDelivered:    0,
		WebhookAttempts:     0,
	}

	_, err = db.Exec(`
		INSERT INTO orders (
			id, app_id, reference_id, network, currency, wallet_address,
			original_amount, original_amount_milli, unique_code,
			total_amount, total_amount_str, total_amount_milli,
			status, qr_path, created_at, expires_at, cooldown_until,
			paid_at, webhook_delivered, webhook_attempts
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0)
	`, order.ID, order.AppID, order.ReferenceID, order.Network, order.Currency, order.WalletAddress,
		order.OriginalAmount, order.OriginalAmountMilli, order.UniqueCode,
		order.TotalAmount, order.TotalAmountStr, order.TotalAmountMilli,
		order.Status, order.QRPath, order.CreatedAt, order.ExpiresAt, order.CooldownUntil,
	)
	if err != nil {
		return nil, fmt.Errorf("gagal menyimpan order ke database: %w", err)
	}

	return order, nil
}

func (s *OrderService) allocateUniqueCode(db *sql.DB, currency string, origMilli int64, amount float64) (int, error) {
	var maxRange int
	if amount < 5.0 {
		maxRange = 25
	} else if amount < 50.0 {
		maxRange = 50
	} else {
		maxRange = 99
	}

	code, found := s.findFreeCode(db, currency, origMilli, 1, maxRange)
	if found {
		return code, nil
	}

	// Emergency Expansion hingga 250 jika tier penuh
	code, found = s.findFreeCode(db, currency, origMilli, 1, 250)
	if found {
		return code, nil
	}

	return 0, fmt.Errorf("semua slot kode unik sedang aktif atau dalam cooldown")
}

func (s *OrderService) findFreeCode(db *sql.DB, currency string, origMilli int64, minCode, maxCode int) (int, bool) {
	minMilli := origMilli + int64(minCode)
	maxMilli := origMilli + int64(maxCode)
	nowMs := time.Now().UnixMilli()

	rows, err := db.Query(`
		SELECT total_amount_milli FROM orders
		WHERE currency = ?
		  AND total_amount_milli BETWEEN ? AND ?
		  AND (
		    status = 'PENDING'
		    OR (status IN ('PAID', 'EXPIRED', 'CANCELLED') AND cooldown_until > ?)
		  )
	`, currency, minMilli, maxMilli, nowMs)
	if err != nil {
		return 0, false
	}
	defer rows.Close()

	occupied := make(map[int64]bool)
	for rows.Next() {
		var m int64
		if err := rows.Scan(&m); err == nil {
			occupied[m] = true
		}
	}

	// Buat list kandidat kode dan shuffle secara acak
	candidates := make([]int, 0, maxCode-minCode+1)
	for c := minCode; c <= maxCode; c++ {
		candidates = append(candidates, c)
	}

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	r.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})

	for _, c := range candidates {
		candMilli := origMilli + int64(c)
		if !occupied[candMilli] {
			return c, true
		}
	}

	return 0, false
}

func (s *OrderService) GetOrderByID(db *sql.DB, orderID string) (*database.Order, error) {
	var ord database.Order
	var txID sql.NullString
	row := db.QueryRow(`
		SELECT id, app_id, reference_id, network, currency, wallet_address,
		       original_amount, original_amount_milli, unique_code, total_amount,
		       total_amount_str, total_amount_milli, status, tx_id, tx_hash,
		       from_address, block_number, qr_path, created_at, expires_at,
		       cooldown_until, paid_at, webhook_delivered, webhook_attempts
		FROM orders WHERE id = ?
	`, orderID)

	err := row.Scan(
		&ord.ID, &ord.AppID, &ord.ReferenceID, &ord.Network, &ord.Currency,
		&ord.WalletAddress, &ord.OriginalAmount, &ord.OriginalAmountMilli,
		&ord.UniqueCode, &ord.TotalAmount, &ord.TotalAmountStr, &ord.TotalAmountMilli,
		&ord.Status, &txID, &ord.TxHash, &ord.FromAddress, &ord.BlockNumber,
		&ord.QRPath, &ord.CreatedAt, &ord.ExpiresAt, &ord.CooldownUntil,
		&ord.PaidAt, &ord.WebhookDelivered, &ord.WebhookAttempts,
	)
	if err != nil {
		return nil, err
	}
	if txID.Valid {
		ord.TxID = &txID.String
	}
	return &ord, nil
}

func (s *OrderService) CancelOrder(db *sql.DB, orderID, appID string) error {
	res, err := db.Exec(`
		UPDATE orders 
		SET status = 'CANCELLED' 
		WHERE id = ? AND app_id = ? AND status = 'PENDING'
	`, orderID, appID)
	if err != nil {
		return err
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		return fmt.Errorf("order tidak ditemukan atau sudah tidak berstatus PENDING")
	}
	return nil
}

func (s *OrderService) SweepExpiredOrders(db *sql.DB) error {
	nowMs := time.Now().UnixMilli()
	_, err := db.Exec(`
		UPDATE orders 
		SET status = 'EXPIRED' 
		WHERE status = 'PENDING' AND expires_at <= ?
	`, nowMs)
	return err
}

func (s *OrderService) ResolveUnmatchedTransaction(db *sql.DB, txID, orderID string, isSandbox bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Cek unmatched tx
	var unTx database.UnmatchedTx
	err = tx.QueryRow(`
		SELECT tx_id, tx_hash, from_address, block_number, resolved_order_id 
		FROM unmatched_transactions WHERE tx_id = ?
	`, txID).Scan(&unTx.TxID, &unTx.TxHash, &unTx.FromAddress, &unTx.BlockNumber, &unTx.ResolvedOrderID)
	if err != nil {
		return fmt.Errorf("mutasi tak dikenal dengan tx_id %s tidak ditemukan: %w", txID, err)
	}
	if strings.TrimSpace(unTx.ResolvedOrderID) != "" {
		return fmt.Errorf("mutasi %s sudah dipasangkan sebelumnya ke order %s", txID, unTx.ResolvedOrderID)
	}

	// 2. Cek order
	var ord database.Order
	err = tx.QueryRow("SELECT id, status FROM orders WHERE id = ?", orderID).Scan(&ord.ID, &ord.Status)
	if err != nil {
		return fmt.Errorf("order %s tidak ditemukan: %w", orderID, err)
	}
	if ord.Status == "PAID" {
		return fmt.Errorf("order %s sudah berstatus PAID", orderID)
	}

	nowMs := time.Now().UnixMilli()

	// 3. Update order menjadi PAID
	_, err = tx.Exec(`
		UPDATE orders 
		SET status = 'PAID', tx_id = ?, tx_hash = ?, from_address = ?, block_number = ?, paid_at = ?
		WHERE id = ?
	`, unTx.TxID, unTx.TxHash, unTx.FromAddress, unTx.BlockNumber, nowMs, orderID)
	if err != nil {
		return fmt.Errorf("gagal mengupdate order menjadi PAID: %w", err)
	}

	// 4. Update unmatched_transactions resolved_order_id
	_, err = tx.Exec(`
		UPDATE unmatched_transactions 
		SET resolved_order_id = ? 
		WHERE tx_id = ?
	`, orderID, txID)
	if err != nil {
		return fmt.Errorf("gagal menandai mutasi: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gagal commit transaksi matching: %w", err)
	}

	// 5. Picu Webhook secara asinkron
	go func() {
		_ = s.webhookSvc.DispatchOrderWebhook(db, orderID, isSandbox)
	}()

	return nil
}
