package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"paymentg-bsc/internal/config"
	"paymentg-bsc/internal/database"
	"paymentg-bsc/internal/handler"
	"paymentg-bsc/internal/middleware"
	"paymentg-bsc/internal/service"
)

func main() {
	log.Println("==================================================")
	log.Println("  USDT BEP-20 (BNB Smart Chain Gateway)          ")
	log.Println("  100% Non-Custodial Watch-Only Web3 Gateway     ")
	log.Println("==================================================")

	// 1. Inisialisasi Konfigurasi
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("[FATAL] Gagal memuat konfigurasi: %v", err)
	}

	// 2. Inisialisasi Database SQLite WAL (Live & Sandbox)
	liveDB, sandboxDB, err := database.InitDatabases()
	if err != nil {
		log.Fatalf("[FATAL] Gagal inisialisasi database: %v", err)
	}
	defer liveDB.Close()
	defer sandboxDB.Close()

	// 3. Sinkronkan konfigurasi tersimpan di system_config jika ada
	initSystemConfigRuntime(liveDB, cfg)

	// 4. Inisialisasi Service Layer
	qrSvc := service.NewQRService()
	webhookSvc := service.NewWebhookService()
	orderSvc := service.NewOrderService(qrSvc, webhookSvc)
	rpcClient := service.NewMultiRPCClient()
	pollerSvc := service.NewPollerService(liveDB, rpcClient, webhookSvc, orderSvc)

	// 5. Inisialisasi Handler Layer
	adminH := handler.NewAdminHandler(liveDB, orderSvc, rpcClient, pollerSvc, webhookSvc)
	orderH := handler.NewOrderHandler(liveDB, sandboxDB, orderSvc, qrSvc, pollerSvc)
	sandboxH := handler.NewSandboxHandler(sandboxDB, orderSvc, qrSvc, webhookSvc)

	// 6. Jalankan Background Workers (Poller, Expirations, Webhook Retries)
	pollerCtx, cancelPoller := context.WithCancel(context.Background())
	defer cancelPoller()
	go pollerSvc.Start(pollerCtx)

	// 7. Setup HTTP Router Gin
	gin.SetMode(cfg.GinMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(middleware.CORSMiddleware())

	// Static UI Pages
	router.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/dashboard/")
	})
	router.GET("/dashboard", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/dashboard/")
	})
	router.GET("/dashboard/", func(c *gin.Context) {
		c.File("dashboard/index.html")
	})
	router.GET("/sandbox", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/sandbox/")
	})
	router.GET("/sandbox/", func(c *gin.Context) {
		c.File("dashboard/sandbox.html")
	})

	// Public API Endpoints
	api := router.Group("/api")
	{
		api.GET("/health", adminH.HealthCheck)
		api.POST("/auth/pin", adminH.LoginPIN)

		// Public QR Serving (Auto-heal)
		api.GET("/orders/:id/qr.png", func(c *gin.Context) {
			orderH.ServeQR(c, false)
		})
		api.GET("/sandbox/orders/:id/qr.png", func(c *gin.Context) {
			orderH.ServeQR(c, true)
		})

		// On-demand buyer verification ("Saya Sudah Bayar")
		api.POST("/orders/:id/check", func(c *gin.Context) {
			orderH.CheckOrder(c, false)
		})
		api.POST("/sandbox/orders/:id/check", func(c *gin.Context) {
			orderH.CheckOrder(c, true)
		})

		// Sandbox Simulator Public Endpoints
		sandbox := api.Group("/sandbox")
		{
			sandbox.GET("/credentials", sandboxH.GetCredentials)
			sandbox.GET("/stats", sandboxH.GetStats)
			sandbox.GET("/orders", sandboxH.GetOrders)
			sandbox.POST("/orders/create-demo", sandboxH.CreateDemoOrder)
			sandbox.POST("/orders/:id/pay", sandboxH.Simulate1ClickPay)
			sandbox.POST("/demo-webhook", sandboxH.DemoWebhookReceiver)
		}
	}

	// Admin Protected Endpoints (X-Admin-Key)
	adminProtected := api.Group("", middleware.RequireAdminKey())
	{
		adminProtected.GET("/stats", adminH.GetStats)
		adminProtected.GET("/orders", adminH.GetOrders)
		adminProtected.GET("/unmatched", adminH.GetUnmatched)
		adminProtected.POST("/unmatched/resolve", adminH.ResolveUnmatched)

		adminProtected.GET("/apps", adminH.GetApps)
		adminProtected.POST("/apps", adminH.CreateApp)
		adminProtected.DELETE("/apps/:id", adminH.DeleteApp)

		adminProtected.GET("/config/bsc", adminH.GetBSCConfig)
		adminProtected.PUT("/config/bsc", adminH.UpdateBSCConfig)
		adminProtected.POST("/config/bsc/test", adminH.TestRPC)
	}

	// Merchant Protected Endpoints Live (X-API-Key: ak_live_...)
	liveMerchant := api.Group("", middleware.RequireAPIKey(liveDB, false))
	{
		liveMerchant.POST("/orders", func(c *gin.Context) {
			orderH.CreateOrder(c, false)
		})
		liveMerchant.GET("/orders/:id", func(c *gin.Context) {
			orderH.GetOrder(c, false)
		})
		liveMerchant.POST("/orders/:id/cancel", func(c *gin.Context) {
			orderH.CancelOrder(c, false)
		})
	}

	// Merchant Protected Endpoints Sandbox (X-API-Key: ak_test_...)
	sandboxMerchant := api.Group("/sandbox", middleware.RequireAPIKey(sandboxDB, true))
	{
		sandboxMerchant.POST("/orders", func(c *gin.Context) {
			orderH.CreateOrder(c, true)
		})
		sandboxMerchant.GET("/orders/:id", func(c *gin.Context) {
			orderH.GetOrder(c, true)
		})
		sandboxMerchant.POST("/orders/:id/cancel", func(c *gin.Context) {
			orderH.CancelOrder(c, true)
		})
	}

	// 8. Menjalankan HTTP Server
	serverAddr := ":" + cfg.Port
	srv := &http.Server{
		Addr:         serverAddr,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("[HTTP Server] Berjalan di port %s ...", cfg.Port)
		log.Printf("[Dashboard] Live UI: http://localhost:%s/dashboard/", cfg.Port)
		log.Printf("[Sandbox]   Simulator: http://localhost:%s/sandbox/", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[FATAL] Gagal mendengarkan HTTP server: %v", err)
		}
	}()

	// 9. Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("[Shutdown] Menerima sinyal terminasi, mematikan server secara graceful...")
	cancelPoller()

	ctxShutdown, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()

	if err := srv.Shutdown(ctxShutdown); err != nil {
		log.Printf("[Shutdown Error] HTTP server shutdown error: %v", err)
	}

	log.Println("[Shutdown] USDT Gateway selesai dihentikan.")
}

func initSystemConfigRuntime(db *sql.DB, cfg *config.Config) {
	// Sync Wallet
	savedWallet, _ := database.GetSystemConfig(db, "receiver_wallet_address")
	if savedWallet != "" {
		cfg.SetReceiverWallet(savedWallet)
	} else {
		_ = database.SetSystemConfig(db, "receiver_wallet_address", cfg.GetReceiverWallet())
	}

	// Sync RPC URLs
	savedRPC, _ := database.GetSystemConfig(db, "bsc_rpc_urls")
	if savedRPC != "" {
		urls := strings.Split(savedRPC, ",")
		var clean []string
		for _, u := range urls {
			trimmed := strings.TrimSpace(u)
			if trimmed != "" {
				clean = append(clean, trimmed)
			}
		}
		if len(clean) > 0 {
			cfg.SetRPCURLs(clean)
		}
	} else {
		_ = database.SetSystemConfig(db, "bsc_rpc_urls", strings.Join(cfg.GetRPCURLs(), ","))
	}

	// Sync Required Confirmations
	savedConf, _ := database.GetSystemConfig(db, "required_confirmations")
	if savedConf != "" {
		if c, err := strconv.ParseUint(savedConf, 10, 64); err == nil && c >= 1 {
			cfg.SetRequiredConfirmations(c)
		}
	} else {
		_ = database.SetSystemConfig(db, "required_confirmations", fmt.Sprintf("%d", cfg.GetRequiredConfirmations()))
	}
}
