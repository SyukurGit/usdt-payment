package handler

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"paymentg-bsc/internal/config"
	"paymentg-bsc/internal/database"
	"paymentg-bsc/internal/service"
)

type OrderHandler struct {
	liveDB    *sql.DB
	sandboxDB *sql.DB
	orderSvc  *service.OrderService
	qrService *service.QRService
	pollerSvc *service.PollerService
}

func NewOrderHandler(liveDB, sandboxDB *sql.DB, orderSvc *service.OrderService, qrService *service.QRService, pollerSvc *service.PollerService) *OrderHandler {
	return &OrderHandler{
		liveDB:    liveDB,
		sandboxDB: sandboxDB,
		orderSvc:  orderSvc,
		qrService: qrService,
		pollerSvc: pollerSvc,
	}
}

func (h *OrderHandler) getDB(isSandbox bool) *sql.DB {
	if isSandbox {
		return h.sandboxDB
	}
	return h.liveDB
}

func (h *OrderHandler) getBaseURL(c *gin.Context) string {
	proto := c.GetHeader("X-Forwarded-Proto")
	if proto == "" {
		if c.Request.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	host := c.GetHeader("X-Forwarded-Host")
	if host == "" {
		host = c.Request.Host
	}
	return fmt.Sprintf("%s://%s", proto, host)
}

func (h *OrderHandler) CreateOrder(c *gin.Context, isSandbox bool) {
	appVal, exists := c.Get("app")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "error": "Unauthorized: API Key invalid"})
		return
	}
	app := appVal.(database.App)

	var req struct {
		ReferenceID string  `json:"reference_id" binding:"required"`
		Amount      float64 `json:"amount" binding:"required"`
		Currency    string  `json:"currency"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "reference_id dan amount wajib diisi."})
		return
	}

	db := h.getDB(isSandbox)
	ord, err := h.orderSvc.CreateOrder(db, service.CreateOrderParams{
		AppID:         app.ID,
		ReferenceID:   strings.TrimSpace(req.ReferenceID),
		Amount:        req.Amount,
		Currency:      req.Currency,
		WalletAddress: config.AppConfig.GetReceiverWallet(),
		IsSandbox:     isSandbox,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	baseURL := h.getBaseURL(c)
	qrEndpoint := "/api/orders/"
	if isSandbox {
		qrEndpoint = "/api/sandbox/orders/"
	}
	qrURL := fmt.Sprintf("%s%s%s/qr.png", baseURL, qrEndpoint, ord.ID)

	expiresAtRFC := time.UnixMilli(ord.ExpiresAt).UTC().Format(time.RFC3339)
	expiresInSec := int((ord.ExpiresAt - time.Now().UnixMilli()) / 1000)
	if expiresInSec < 0 {
		expiresInSec = 0
	}

	cexWarning := fmt.Sprintf(
		"PENTING: Hanya kirim USDT melalui jaringan BNB Smart Chain (BEP-20). Jika mengirim dari Exchange (Indodax/Tokocrypto/Bybit/Binance), pastikan Jumlah Bersih Diterima (Receive Amount) adalah TEPAT %s USDT setelah dikurangi biaya penarikan.",
		ord.TotalAmountStr,
	)

	statusHTTP := http.StatusCreated
	if ord.CreatedAt < time.Now().UnixMilli()-2000 {
		statusHTTP = http.StatusOK // Idempotent repeat
	}

	c.JSON(statusHTTP, gin.H{
		"success": true,
		"data": gin.H{
			"order_id":           ord.ID,
			"reference_id":       ord.ReferenceID,
			"network":            ord.Network,
			"chain_id":           config.AppConfig.ChainID,
			"currency":           ord.Currency,
			"token_contract":     config.AppConfig.USDTContractAddress,
			"wallet_address":     ord.WalletAddress,
			"original_amount":    ord.OriginalAmount,
			"unique_code":        ord.UniqueCode,
			"total_amount":       ord.TotalAmount,
			"total_amount_str":   ord.TotalAmountStr,
			"cex_warning":        cexWarning,
			"status":             ord.Status,
			"qr_url":             qrURL,
			"expires_at":         expiresAtRFC,
			"expires_in_seconds": expiresInSec,
		},
	})
}

func (h *OrderHandler) GetOrder(c *gin.Context, isSandbox bool) {
	orderID := c.Param("id")
	db := h.getDB(isSandbox)

	ord, err := h.orderSvc.GetOrderByID(db, orderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "Order tidak ditemukan."})
		return
	}

	// Jika request memiliki API Key, pastikan app_id cocok
	if appVal, exists := c.Get("app"); exists {
		app := appVal.(database.App)
		if ord.AppID != app.ID {
			c.JSON(http.StatusForbidden, gin.H{"success": false, "error": "Akses ditolak: Order bukan milik aplikasi ini."})
			return
		}
	}

	baseURL := h.getBaseURL(c)
	qrEndpoint := "/api/orders/"
	if isSandbox {
		qrEndpoint = "/api/sandbox/orders/"
	}
	qrURL := fmt.Sprintf("%s%s%s/qr.png", baseURL, qrEndpoint, ord.ID)

	expiresAtRFC := time.UnixMilli(ord.ExpiresAt).UTC().Format(time.RFC3339)
	var paidAtRFC *string
	if ord.PaidAt > 0 {
		p := time.UnixMilli(ord.PaidAt).UTC().Format(time.RFC3339)
		paidAtRFC = &p
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"order_id":         ord.ID,
			"reference_id":     ord.ReferenceID,
			"network":          ord.Network,
			"chain_id":         config.AppConfig.ChainID,
			"currency":         ord.Currency,
			"wallet_address":   ord.WalletAddress,
			"original_amount":  ord.OriginalAmount,
			"unique_code":      ord.UniqueCode,
			"total_amount":     ord.TotalAmount,
			"total_amount_str": ord.TotalAmountStr,
			"status":           ord.Status,
			"tx_id":            ord.TxID,
			"tx_hash":          ord.TxHash,
			"from_address":     ord.FromAddress,
			"block_number":     ord.BlockNumber,
			"qr_url":           qrURL,
			"expires_at":       expiresAtRFC,
			"paid_at":          paidAtRFC,
		},
	})
}

func (h *OrderHandler) ServeQR(c *gin.Context, isSandbox bool) {
	orderID := c.Param("id")
	db := h.getDB(isSandbox)

	var walletAddress string
	err := db.QueryRow("SELECT wallet_address FROM orders WHERE id = ?", orderID).Scan(&walletAddress)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "Order tidak ditemukan."})
		return
	}

	qrPath, err := h.qrService.EnsureQRCode(orderID, walletAddress, isSandbox)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "Gagal memproses QR code."})
		return
	}

	c.Header("Content-Type", "image/png")
	c.Header("Cache-Control", "public, max-age=3600")
	c.File(qrPath)
}

func (h *OrderHandler) CheckOrder(c *gin.Context, isSandbox bool) {
	orderID := c.Param("id")
	db := h.getDB(isSandbox)

	ord, err := h.orderSvc.GetOrderByID(db, orderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "Order tidak ditemukan."})
		return
	}

	scannedNow := false
	nextCheckInSec := 0

	// Jika Live dan status masih PENDING, picu smart rate-gate scan
	if !isSandbox && ord.Status == "PENDING" {
		scanned, remaining := h.pollerSvc.TriggerOnDemandCheck(c.Request.Context())
		scannedNow = scanned
		nextCheckInSec = remaining

		if scanned {
			// Reload order status setelah scan terbaru
			if updated, err := h.orderSvc.GetOrderByID(db, orderID); err == nil {
				ord = updated
			}
		}
	}

	var paidAtRFC *string
	if ord.PaidAt > 0 {
		p := time.UnixMilli(ord.PaidAt).UTC().Format(time.RFC3339)
		paidAtRFC = &p
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"order_id":               ord.ID,
			"status":                 ord.Status,
			"total_amount_str":       ord.TotalAmountStr,
			"tx_hash":                ord.TxHash,
			"paid_at":                paidAtRFC,
			"scanned_now":            scannedNow,
			"next_check_in_seconds": nextCheckInSec,
		},
	})
}

func (h *OrderHandler) CancelOrder(c *gin.Context, isSandbox bool) {
	orderID := c.Param("id")
	db := h.getDB(isSandbox)

	appVal, exists := c.Get("app")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "error": "Unauthorized: API Key invalid"})
		return
	}
	app := appVal.(database.App)

	err := h.orderSvc.CancelOrder(db, orderID, app.ID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Pesanan berhasil dibatalkan."})
}
