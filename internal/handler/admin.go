package handler

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"paymentg-bsc/internal/config"
	"paymentg-bsc/internal/database"
	"paymentg-bsc/internal/middleware"
	"paymentg-bsc/internal/service"
)

type AdminHandler struct {
	db         *sql.DB
	orderSvc   *service.OrderService
	rpcClient  *service.MultiRPCClient
	pollerSvc  *service.PollerService
	webhookSvc *service.WebhookService
}

func NewAdminHandler(db *sql.DB, orderSvc *service.OrderService, rpcClient *service.MultiRPCClient, pollerSvc *service.PollerService, webhookSvc *service.WebhookService) *AdminHandler {
	return &AdminHandler{
		db:         db,
		orderSvc:   orderSvc,
		rpcClient:  rpcClient,
		pollerSvc:  pollerSvc,
		webhookSvc: webhookSvc,
	}
}

func (h *AdminHandler) LoginPIN(c *gin.Context) {
	var req struct {
		PIN string `json:"pin" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "PIN wajib diisi."})
		return
	}

	valid, msg, waitTime := middleware.ValidateAdminPIN(req.PIN)
	if !valid {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success":     false,
			"error":       msg,
			"retry_after": waitTime,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"token":      config.AppConfig.AdminKey,
			"expires_in": 86400,
		},
	})
}

func (h *AdminHandler) HealthCheck(c *gin.Context) {
	latestBlock, activeRPC, err := h.rpcClient.GetLatestBlockNumber(c.Request.Context())
	status := "ok"
	if err != nil {
		status = "degraded"
	}

	c.JSON(http.StatusOK, gin.H{
		"status":          status,
		"network":         config.AppConfig.NetworkName,
		"chain_id":        config.AppConfig.ChainID,
		"active_rpc":      activeRPC,
		"latest_block":    latestBlock,
		"receiver_wallet": config.AppConfig.GetReceiverWallet(),
		"usdt_contract":   config.AppConfig.USDTContractAddress,
	})
}

func (h *AdminHandler) GetStats(c *gin.Context) {
	var volume sql.NullFloat64
	var paidCount, pendingCount, expiredCount, unmatchedCount int

	_ = h.db.QueryRow("SELECT SUM(total_amount) FROM orders WHERE status = 'PAID'").Scan(&volume)
	_ = h.db.QueryRow("SELECT COUNT(*) FROM orders WHERE status = 'PAID'").Scan(&paidCount)
	_ = h.db.QueryRow("SELECT COUNT(*) FROM orders WHERE status = 'PENDING'").Scan(&pendingCount)
	_ = h.db.QueryRow("SELECT COUNT(*) FROM orders WHERE status = 'EXPIRED'").Scan(&expiredCount)
	_ = h.db.QueryRow("SELECT COUNT(*) FROM unmatched_transactions WHERE resolved_order_id = ''").Scan(&unmatchedCount)

	lastScanned, _ := database.GetSystemConfig(h.db, "last_scanned_block")

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"total_volume_usdt":    volume.Float64,
			"total_orders_paid":    paidCount,
			"total_orders_pending": pendingCount,
			"total_orders_expired": expiredCount,
			"total_unmatched":      unmatchedCount,
			"last_scanned_block":   lastScanned,
			"active_rpc":           h.rpcClient.GetActiveRPC(),
			"receiver_wallet":      config.AppConfig.GetReceiverWallet(),
		},
	})
}

func (h *AdminHandler) GetOrders(c *gin.Context) {
	statusFilter := strings.TrimSpace(c.Query("status"))

	var rows *sql.Rows
	var err error

	if statusFilter != "" {
		rows, err = h.db.Query(`
			SELECT id, app_id, reference_id, network, currency, wallet_address,
			       original_amount, unique_code, total_amount, total_amount_str,
			       status, tx_id, tx_hash, from_address, block_number, qr_path,
			       created_at, expires_at, paid_at, webhook_delivered, webhook_attempts
			FROM orders
			WHERE status = ?
			ORDER BY created_at DESC LIMIT 100
		`, statusFilter)
	} else {
		rows, err = h.db.Query(`
			SELECT id, app_id, reference_id, network, currency, wallet_address,
			       original_amount, unique_code, total_amount, total_amount_str,
			       status, tx_id, tx_hash, from_address, block_number, qr_path,
			       created_at, expires_at, paid_at, webhook_delivered, webhook_attempts
			FROM orders
			ORDER BY created_at DESC LIMIT 100
		`)
	}

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

func (h *AdminHandler) GetUnmatched(c *gin.Context) {
	rows, err := h.db.Query(`
		SELECT tx_id, tx_hash, from_address, currency, amount_str, amount_milli,
		       raw_wei, block_number, tx_time, resolved_order_id, recorded_at
		FROM unmatched_transactions
		ORDER BY tx_time DESC LIMIT 100
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}
	defer rows.Close()

	list := []database.UnmatchedTx{}
	for rows.Next() {
		var u database.UnmatchedTx
		if err := rows.Scan(
			&u.TxID, &u.TxHash, &u.FromAddress, &u.Currency, &u.AmountStr, &u.AmountMilli,
			&u.RawWei, &u.BlockNumber, &u.TxTime, &u.ResolvedOrderID, &u.RecordedAt,
		); err == nil {
			list = append(list, u)
		}
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": list})
}

func (h *AdminHandler) ResolveUnmatched(c *gin.Context) {
	var req struct {
		TxID    string `json:"tx_id" binding:"required"`
		OrderID string `json:"order_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "tx_id dan order_id wajib diisi."})
		return
	}

	err := h.orderSvc.ResolveUnmatchedTransaction(h.db, req.TxID, req.OrderID, false)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Mutasi on-chain berhasil dipasangkan ke order dan webhook dikirim.",
	})
}

func (h *AdminHandler) GetApps(c *gin.Context) {
	rows, err := h.db.Query("SELECT id, name, api_key, webhook_url, created_at FROM apps ORDER BY created_at DESC")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}
	defer rows.Close()

	type AppItem struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		APIKey        string `json:"api_key"`
		LiveAPIKey    string `json:"live_api_key"`
		SandboxAPIKey string `json:"sandbox_api_key"`
		WebhookURL    string `json:"webhook_url"`
		CreatedAt     string `json:"created_at"`
	}

	apps := []AppItem{}
	for rows.Next() {
		var a database.App
		if err := rows.Scan(&a.ID, &a.Name, &a.APIKey, &a.WebhookURL, &a.CreatedAt); err == nil {
			var sandKey string
			if database.SandboxDB != nil {
				_ = database.SandboxDB.QueryRow(
					"SELECT api_key FROM apps WHERE name = ? OR name = ? ORDER BY created_at DESC LIMIT 1",
					a.Name, a.Name+" (Sandbox)",
				).Scan(&sandKey)
			}
			apps = append(apps, AppItem{
				ID:            a.ID,
				Name:          a.Name,
				APIKey:        a.APIKey,
				LiveAPIKey:    a.APIKey,
				SandboxAPIKey: sandKey,
				WebhookURL:    a.WebhookURL,
				CreatedAt:     a.CreatedAt,
			})
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": apps})
}

func (h *AdminHandler) CreateApp(c *gin.Context) {
	var req struct {
		Name              string `json:"name" binding:"required"`
		WebhookURL        string `json:"webhook_url" binding:"required"`
		SandboxWebhookURL string `json:"sandbox_webhook_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "name dan webhook_url wajib diisi."})
		return
	}

	liveApp, sandboxApp, err := database.CreateAppPair(req.Name, req.WebhookURL, req.SandboxWebhookURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"data": gin.H{
			"id":              liveApp.ID,
			"name":            liveApp.Name,
			"api_key":         liveApp.APIKey,
			"live_api_key":    liveApp.APIKey,
			"sandbox_api_key": sandboxApp.APIKey,
			"webhook_url":     liveApp.WebhookURL,
			"created_at":      liveApp.CreatedAt,
		},
	})
}

func (h *AdminHandler) DeleteApp(c *gin.Context) {
	appID := c.Param("id")
	if err := database.DeleteAppPair(appID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Aplikasi/Toko berhasil dihapus."})
}

func (h *AdminHandler) GetBSCConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"receiver_wallet":        config.AppConfig.GetReceiverWallet(),
			"rpc_urls":               config.AppConfig.GetRPCURLs(),
			"required_confirmations": config.AppConfig.GetRequiredConfirmations(),
			"usdt_contract":          config.AppConfig.USDTContractAddress,
			"usdt_decimals":          config.AppConfig.USDTDecimals,
			"network_name":           config.AppConfig.NetworkName,
			"chain_id":               config.AppConfig.ChainID,
		},
	})
}

func (h *AdminHandler) UpdateBSCConfig(c *gin.Context) {
	var req struct {
		ReceiverWallet        *string  `json:"receiver_wallet"`
		RPCURLs               []string `json:"rpc_urls"`
		RequiredConfirmations *uint64  `json:"required_confirmations"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Payload tidak valid."})
		return
	}

	if req.ReceiverWallet != nil {
		w := strings.TrimSpace(*req.ReceiverWallet)
		if !strings.HasPrefix(w, "0x") || len(w) != 42 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Alamat wallet penerima wajib 42 karakter heksadesimal diawali 0x."})
			return
		}
		config.AppConfig.SetReceiverWallet(w)
		_ = database.SetSystemConfig(h.db, "receiver_wallet_address", w)
		_ = database.SetSystemConfig(database.SandboxDB, "receiver_wallet_address", w)
	}

	if len(req.RPCURLs) > 0 {
		var validURLs []string
		for _, u := range req.RPCURLs {
			trimmed := strings.TrimSpace(u)
			if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
				validURLs = append(validURLs, trimmed)
			}
		}
		if len(validURLs) > 0 {
			config.AppConfig.SetRPCURLs(validURLs)
			_ = database.SetSystemConfig(h.db, "bsc_rpc_urls", strings.Join(validURLs, ","))
			_ = database.SetSystemConfig(database.SandboxDB, "bsc_rpc_urls", strings.Join(validURLs, ","))
		}
	}

	if req.RequiredConfirmations != nil && *req.RequiredConfirmations >= 1 {
		config.AppConfig.SetRequiredConfirmations(*req.RequiredConfirmations)
		_ = database.SetSystemConfig(h.db, "required_confirmations", strconv.FormatUint(*req.RequiredConfirmations, 10))
		_ = database.SetSystemConfig(database.SandboxDB, "required_confirmations", strconv.FormatUint(*req.RequiredConfirmations, 10))
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Konfigurasi runtime BSC berhasil diperbarui.",
		"data": gin.H{
			"receiver_wallet":        config.AppConfig.GetReceiverWallet(),
			"rpc_urls":               config.AppConfig.GetRPCURLs(),
			"required_confirmations": config.AppConfig.GetRequiredConfirmations(),
		},
	})
}

func (h *AdminHandler) TestRPC(c *gin.Context) {
	latestBlock, activeRPC, latency, err := h.rpcClient.TestRPCConnection(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Tes koneksi RPC gagal: %v", err),
			"latency": latency,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"latest_block": latestBlock,
			"active_rpc":   activeRPC,
			"latency_ms":   latency,
		},
	})
}
