package service

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"paymentg-bsc/internal/database"
)

type WebhookPayload struct {
	Event          string  `json:"event"`
	IsSandbox      bool    `json:"is_sandbox"`
	OrderID        string  `json:"order_id"`
	ReferenceID    string  `json:"reference_id"`
	Network        string  `json:"network"`
	Currency       string  `json:"currency"`
	OriginalAmount float64 `json:"original_amount"`
	UniqueCode     int     `json:"unique_code"`
	TotalAmount    float64 `json:"total_amount"`
	TotalAmountStr string  `json:"total_amount_str"`
	WalletAddress  string  `json:"wallet_address"`
	FromAddress    string  `json:"from_address"`
	TxHash         string  `json:"tx_hash"`
	BlockNumber    uint64  `json:"block_number"`
	PaidAt         string  `json:"paid_at"`
	Timestamp      int64   `json:"timestamp"`
}

type WebhookService struct {
	httpClient *http.Client
	inFlight   sync.Map // orderID -> struct{}
}

func NewWebhookService() *WebhookService {
	return &WebhookService{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (s *WebhookService) ComputeHMAC(data []byte, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func (s *WebhookService) DispatchOrderWebhook(db *sql.DB, orderID string, isSandbox bool) error {
	// In-Flight Lock untuk mencegah duplicate send paralel
	if _, loaded := s.inFlight.LoadOrStore(orderID, struct{}{}); loaded {
		return nil
	}
	defer s.inFlight.Delete(orderID)

	var ord database.Order
	var txID sql.NullString
	row := db.QueryRow(`
		SELECT id, app_id, reference_id, network, currency, wallet_address,
		       original_amount, unique_code, total_amount, total_amount_str,
		       status, tx_id, tx_hash, from_address, block_number, paid_at,
		       webhook_delivered, webhook_attempts
		FROM orders WHERE id = ?
	`, orderID)

	err := row.Scan(
		&ord.ID, &ord.AppID, &ord.ReferenceID, &ord.Network, &ord.Currency, &ord.WalletAddress,
		&ord.OriginalAmount, &ord.UniqueCode, &ord.TotalAmount, &ord.TotalAmountStr,
		&ord.Status, &txID, &ord.TxHash, &ord.FromAddress, &ord.BlockNumber, &ord.PaidAt,
		&ord.WebhookDelivered, &ord.WebhookAttempts,
	)
	if err != nil {
		return fmt.Errorf("failed to load order %s: %w", orderID, err)
	}

	if ord.Status != "PAID" {
		return fmt.Errorf("order %s is not paid (status: %s)", orderID, ord.Status)
	}
	if ord.WebhookDelivered == 1 {
		return nil // Sudah terkirim sukses sebelumnya
	}

	// Ambil data App (API Key & Webhook URL)
	var app database.App
	err = db.QueryRow("SELECT id, name, api_key, webhook_url FROM apps WHERE id = ?", ord.AppID).
		Scan(&app.ID, &app.Name, &app.APIKey, &app.WebhookURL)
	if err != nil {
		return fmt.Errorf("failed to load app %s for order %s: %w", ord.AppID, orderID, err)
	}

	if app.WebhookURL == "" {
		log.Printf("[Webhook] Order %s: app %s tidak memiliki Webhook URL, skip.", orderID, app.ID)
		return nil
	}

	paidAtStr := time.UnixMilli(ord.PaidAt).UTC().Format(time.RFC3339)
	nowUnix := time.Now().Unix()

	payload := WebhookPayload{
		Event:          "payment.success",
		IsSandbox:      isSandbox,
		OrderID:        ord.ID,
		ReferenceID:    ord.ReferenceID,
		Network:        ord.Network,
		Currency:       ord.Currency,
		OriginalAmount: ord.OriginalAmount,
		UniqueCode:     ord.UniqueCode,
		TotalAmount:    ord.TotalAmount,
		TotalAmountStr: ord.TotalAmountStr,
		WalletAddress:  ord.WalletAddress,
		FromAddress:    ord.FromAddress,
		TxHash:         ord.TxHash,
		BlockNumber:    ord.BlockNumber,
		PaidAt:         paidAtStr,
		Timestamp:      nowUnix,
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	sig := s.ComputeHMAC(jsonBytes, app.APIKey)

	req, err := http.NewRequest(http.MethodPost, app.WebhookURL, bytes.NewReader(jsonBytes))
	if err != nil {
		return fmt.Errorf("failed to create http request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Event", "payment.success")
	req.Header.Set("X-Webhook-Timestamp", fmt.Sprintf("%d", nowUnix))
	req.Header.Set("X-Webhook-Signature", sig)

	resp, err := s.httpClient.Do(req)
	newAttempts := ord.WebhookAttempts + 1

	if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		_, _ = db.Exec(`
			UPDATE orders 
			SET webhook_delivered = 1, webhook_attempts = ?, next_webhook_retry_at = 0 
			WHERE id = ?
		`, newAttempts, orderID)
		log.Printf("[Webhook Success] Order %s delivered to %s (HTTP %d)", orderID, app.WebhookURL, resp.StatusCode)
		return nil
	}

	var statusInfo string
	if resp != nil {
		statusInfo = fmt.Sprintf("HTTP %d", resp.StatusCode)
		resp.Body.Close()
	} else {
		statusInfo = fmt.Sprintf("Error: %v", err)
	}

	// Hitung backoff
	backoffs := []int64{10, 30, 60, 300, 900}
	var backoffSec int64 = 900
	if newAttempts-1 < len(backoffs) {
		backoffSec = backoffs[newAttempts-1]
	}
	nextRetry := time.Now().UnixMilli() + (backoffSec * 1000)

	_, _ = db.Exec(`
		UPDATE orders 
		SET webhook_delivered = 0, webhook_attempts = ?, next_webhook_retry_at = ? 
		WHERE id = ?
	`, newAttempts, nextRetry, orderID)

	log.Printf("[Webhook Failed] Order %s attempt #%d failed (%s), retry in %ds", orderID, newAttempts, statusInfo, backoffSec)
	return fmt.Errorf("webhook failed (%s)", statusInfo)
}

func (s *WebhookService) RetryPendingWebhooks(db *sql.DB, isSandbox bool) {
	nowMs := time.Now().UnixMilli()
	rows, err := db.Query(`
		SELECT id FROM orders 
		WHERE status = 'PAID' 
		  AND webhook_delivered = 0 
		  AND next_webhook_retry_at <= ? 
		  AND webhook_attempts < 5
		LIMIT 20
	`, nowMs)
	if err != nil {
		return
	}
	defer rows.Close()

	var orderIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			orderIDs = append(orderIDs, id)
		}
	}

	for _, id := range orderIDs {
		go func(ordID string) {
			_ = s.DispatchOrderWebhook(db, ordID, isSandbox)
		}(id)
	}
}
