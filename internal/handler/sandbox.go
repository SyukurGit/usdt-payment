package handler

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"paymentg-bsc/internal/config"
	"paymentg-bsc/internal/database"
	"paymentg-bsc/internal/service"
)

type SandboxHandler struct {
	sandboxDB  *sql.DB
	orderSvc   *service.OrderService
	qrService  *service.QRService
	webhookSvc *service.WebhookService
}

func NewSandboxHandler(sandboxDB *sql.DB, orderSvc *service.OrderService, qrService *service.QRService, webhookSvc *service.WebhookService) *SandboxHandler {
	return &SandboxHandler{
		sandboxDB:  sandboxDB,
		orderSvc:   orderSvc,
		qrService:  qrService,
		webhookSvc: webhookSvc,
	}
}

func (h *SandboxHandler) GetCredentials(c *gin.Context) {
	var app database.App
	err := h.sandboxDB.QueryRow("SELECT id, name, api_key, webhook_url, created_at FROM apps ORDER BY created_at ASC LIMIT 1").
		Scan(&app.ID, &app.Name, &app.APIKey, &app.WebhookURL, &app.CreatedAt)

	if err != nil {
		// Buat demo store jika belum ada
		newApp, createErr := database.CreateAppDual("Demo Sandbox Store", "http://localhost:3400/api/sandbox/demo-webhook")
		if createErr == nil {
			app = *newApp
		}
	}

	host := c.Request.Host
	proto := "http"
	if c.Request.TLS != nil {
		proto = "https"
	}
	baseURL := fmt.Sprintf("%s://%s", proto, host)

	sampleCurl := fmt.Sprintf(`curl -X POST %s/api/sandbox/orders \
  -H "Content-Type: application/json" \
  -H "X-API-Key: %s" \
  -d '{"reference_id": "INV-%d", "amount": 0.8}'`, baseURL, app.APIKey, time.Now().Unix()%10000)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"app_id":      app.ID,
			"app_name":    app.Name,
			"api_key":     app.APIKey,
			"webhook_url": app.WebhookURL,
			"sample_curl": sampleCurl,
		},
	})
}

func (h *SandboxHandler) GetStats(c *gin.Context) {
	var volume sql.NullFloat64
	var paidCount, pendingCount, expiredCount int

	_ = h.sandboxDB.QueryRow("SELECT SUM(total_amount) FROM orders WHERE status = 'PAID'").Scan(&volume)
	_ = h.sandboxDB.QueryRow("SELECT COUNT(*) FROM orders WHERE status = 'PAID'").Scan(&paidCount)
	_ = h.sandboxDB.QueryRow("SELECT COUNT(*) FROM orders WHERE status = 'PENDING'").Scan(&pendingCount)
	_ = h.sandboxDB.QueryRow("SELECT COUNT(*) FROM orders WHERE status = 'EXPIRED'").Scan(&expiredCount)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"total_volume_usdt":    volume.Float64,
			"total_orders_paid":    paidCount,
			"total_orders_pending": pendingCount,
			"total_orders_expired": expiredCount,
		},
	})
}

func (h *SandboxHandler) GetOrders(c *gin.Context) {
	rows, err := h.sandboxDB.Query(`
		SELECT id, app_id, reference_id, network, currency, wallet_address,
		       original_amount, unique_code, total_amount, total_amount_str,
		       status, tx_id, tx_hash, from_address, block_number, qr_path,
		       created_at, expires_at, paid_at, webhook_delivered, webhook_attempts
		FROM orders
		ORDER BY created_at DESC LIMIT 100
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}
	defer rows.Close()

	orders := []gin.H{}
	for rows.Next() {
		var o database.Order
		var txID sql.NullString
		err := rows.Scan(
			&o.ID, &o.AppID, &o.ReferenceID, &o.Network, &o.Currency, &o.WalletAddress,
			&o.OriginalAmount, &o.UniqueCode, &o.TotalAmount, &o.TotalAmountStr,
			&o.Status, &txID, &o.TxHash, &o.FromAddress, &o.BlockNumber, &o.QRPath,
			&o.CreatedAt, &o.ExpiresAt, &o.PaidAt, &o.WebhookDelivered, &o.WebhookAttempts,
		)
		if err == nil {
			var txIDVal *string
			if txID.Valid {
				txIDVal = &txID.String
			}
			orders = append(orders, gin.H{
				"id":                o.ID,
				"app_id":            o.AppID,
				"reference_id":      o.ReferenceID,
				"network":           o.Network,
				"currency":          o.Currency,
				"wallet_address":    o.WalletAddress,
				"original_amount":   o.OriginalAmount,
				"unique_code":       o.UniqueCode,
				"total_amount":      o.TotalAmount,
				"total_amount_str":  o.TotalAmountStr,
				"status":            o.Status,
				"tx_id":             txIDVal,
				"tx_hash":           o.TxHash,
				"from_address":      o.FromAddress,
				"block_number":      o.BlockNumber,
				"created_at":        o.CreatedAt,
				"expires_at":        o.ExpiresAt,
				"paid_at":           o.PaidAt,
				"webhook_delivered": o.WebhookDelivered,
				"webhook_attempts":  o.WebhookAttempts,
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": orders})
}

func (h *SandboxHandler) CreateDemoOrder(c *gin.Context) {
	var req struct {
		Amount      float64 `json:"amount"`
		ReferenceID string  `json:"reference_id"`
	}
	_ = c.ShouldBindJSON(&req)

	if req.Amount <= 0 {
		req.Amount = 0.8
	}
	if strings.TrimSpace(req.ReferenceID) == "" {
		req.ReferenceID = fmt.Sprintf("DEMO-%d", time.Now().Unix()%100000)
	}

	// Cari app pertama
	var app database.App
	err := h.sandboxDB.QueryRow("SELECT id, name, api_key, webhook_url, created_at FROM apps ORDER BY created_at ASC LIMIT 1").
		Scan(&app.ID, &app.Name, &app.APIKey, &app.WebhookURL, &app.CreatedAt)
	if err != nil {
		newApp, _ := database.CreateAppDual("Demo Sandbox Store", "http://localhost:3400/api/sandbox/demo-webhook")
		app = *newApp
	}

	ord, err := h.orderSvc.CreateOrder(h.sandboxDB, service.CreateOrderParams{
		AppID:         app.ID,
		ReferenceID:   req.ReferenceID,
		Amount:        req.Amount,
		Currency:      "USDT",
		WalletAddress: config.AppConfig.GetReceiverWallet(),
		IsSandbox:     true,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"data": gin.H{
			"order_id":         ord.ID,
			"reference_id":     ord.ReferenceID,
			"total_amount_str": ord.TotalAmountStr,
			"status":           ord.Status,
		},
	})
}

func (h *SandboxHandler) Simulate1ClickPay(c *gin.Context) {
	orderID := c.Param("id")

	var ord database.Order
	err := h.sandboxDB.QueryRow(`
		SELECT id, app_id, reference_id, total_amount_str, status 
		FROM orders WHERE id = ?
	`, orderID).Scan(&ord.ID, &ord.AppID, &ord.ReferenceID, &ord.TotalAmountStr, &ord.Status)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "Order sandbox tidak ditemukan."})
		return
	}

	if ord.Status == "PAID" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Order ini sudah berstatus PAID sebelumnya."})
		return
	}

	// Buat simulated tx_hash 66 karakter
	simTxHash := "0xsandbox" + database.RandomHex(28)
	simTxID := simTxHash + "_0"
	simFromAddress := "0xDem0000000000000000000000000000000000001"
	simBlockNumber := uint64(43120950)
	nowMs := time.Now().UnixMilli()

	_, err = h.sandboxDB.Exec(`
		UPDATE orders 
		SET status = 'PAID', tx_id = ?, tx_hash = ?, from_address = ?, block_number = ?, paid_at = ?
		WHERE id = ?
	`, simTxID, simTxHash, simFromAddress, simBlockNumber, nowMs, orderID)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "Gagal mengupdate order sandbox: " + err.Error()})
		return
	}

	// Picu webhook secara instan
	go func() {
		_ = h.webhookSvc.DispatchOrderWebhook(h.sandboxDB, orderID, true)
	}()

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Simulasi transfer on-chain 1-klik berhasil! Status diubah menjadi PAID dan webhook telah ditembakkan.",
		"data": gin.H{
			"order_id":     orderID,
			"status":       "PAID",
			"tx_hash":      simTxHash,
			"from_address": simFromAddress,
			"block_number": simBlockNumber,
			"paid_at":      time.UnixMilli(nowMs).UTC().Format(time.RFC3339),
		},
	})
}

func (h *SandboxHandler) DemoWebhookReceiver(c *gin.Context) {
	body, _ := io.ReadAll(c.Request.Body)
	event := c.GetHeader("X-Webhook-Event")
	timestamp := c.GetHeader("X-Webhook-Timestamp")
	signature := c.GetHeader("X-Webhook-Signature")

	log.Printf("[Demo Webhook Received] Event: %s, Ts: %s, Sig: %s\nPayload: %s", event, timestamp, signature, string(body))

	c.JSON(http.StatusOK, gin.H{
		"received":  true,
		"event":     event,
		"timestamp": timestamp,
		"message":   "Demo webhook receiver processed request successfully",
	})
}
