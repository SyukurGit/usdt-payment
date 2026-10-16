package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"paymentg-bsc/internal/config"
	"paymentg-bsc/internal/database"
)

type PollerService struct {
	db           *sql.DB
	rpcClient    *MultiRPCClient
	webhookSvc   *WebhookService
	orderSvc     *OrderService
	mu           sync.Mutex
	lastPollTime time.Time
	isScanning   bool
}

func NewPollerService(db *sql.DB, rpcClient *MultiRPCClient, webhookSvc *WebhookService, orderSvc *OrderService) *PollerService {
	return &PollerService{
		db:         db,
		rpcClient:  rpcClient,
		webhookSvc: webhookSvc,
		orderSvc:   orderSvc,
	}
}

func (p *PollerService) Start(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(config.AppConfig.BackgroundSweepIntervalSec) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[Poller] Worker dihentikan oleh context.")
			return
		case <-ticker.C:
			_ = p.orderSvc.SweepExpiredOrders(p.db)
			p.webhookSvc.RetryPendingWebhooks(p.db, false)
			p.webhookSvc.RetryPendingWebhooks(database.SandboxDB, true)

			active, err := p.HasActiveOrders()
			if err != nil {
				log.Printf("[Poller Check Error] %v", err)
				continue
			}

			if active {
				_ = p.RunScanCycle(ctx)
			}
		}
	}
}

func (p *PollerService) HasActiveOrders() (bool, error) {
	nowMs := time.Now().UnixMilli()
	tailMs := nowMs - (15 * 60 * 1000) // 15 menit ke belakang untuk tail-polling

	var count int
	err := p.db.QueryRow(`
		SELECT COUNT(*) FROM orders 
		WHERE status = 'PENDING' 
		   OR (status = 'EXPIRED' AND expires_at >= ?)
	`, tailMs).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (p *PollerService) RunScanCycle(ctx context.Context) error {
	p.mu.Lock()
	if p.isScanning {
		p.mu.Unlock()
		return nil
	}
	p.isScanning = true
	p.lastPollTime = time.Now()
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.isScanning = false
		p.mu.Unlock()
	}()

	latestBlock, activeRPC, err := p.rpcClient.GetLatestBlockNumber(ctx)
	if err != nil {
		log.Printf("[Poller RPC Error] Gagal mendapatkan eth_blockNumber: %v", err)
		return err
	}

	reqConfirmations := config.AppConfig.GetRequiredConfirmations()
	if latestBlock < reqConfirmations {
		return nil
	}
	safeBlock := latestBlock - reqConfirmations

	// Ambil last_scanned_block dari system_config
	lastScannedStr, _ := database.GetSystemConfig(p.db, "last_scanned_block")
	var lastScanned uint64
	if lastScannedStr != "" {
		lastScanned, _ = strconv.ParseUint(lastScannedStr, 10, 64)
	}

	var fromBlock uint64
	if lastScanned == 0 || safeBlock > lastScanned+1200 {
		// Smart Block Jump jika baru bangun atau tertinggal > 1200 blok (~1 jam)
		if safeBlock > 20 {
			fromBlock = safeBlock - 20
		} else {
			fromBlock = 1
		}
	} else {
		fromBlock = lastScanned + 1
	}

	if fromBlock > safeBlock {
		return nil // Tidak ada blok baru yang sudah final
	}

	maxBatch := config.AppConfig.MaxBlockRangePerQuery
	receiverWallet := config.AppConfig.GetReceiverWallet()

	// Proses blok per batch
	for fromBlock <= safeBlock {
		toBlock := safeBlock
		if toBlock-fromBlock+1 > maxBatch {
			toBlock = fromBlock + maxBatch - 1
		}

		logs, err := p.rpcClient.GetLogs(ctx, fromBlock, toBlock, receiverWallet)
		if err != nil {
			log.Printf("[Poller RPC Error] eth_getLogs [%d..%d] via %s gagal: %v", fromBlock, toBlock, activeRPC, err)
			return err
		}

		for _, item := range logs {
			if item.Removed {
				continue
			}
			p.processLogItem(item)
		}

		// Update cursor
		_ = database.SetSystemConfig(p.db, "last_scanned_block", fmt.Sprintf("%d", toBlock))
		fromBlock = toBlock + 1
	}

	return nil
}

func (p *PollerService) processLogItem(item RPCLogItem) {
	// Verifikasi kontrak
	if !strings.EqualFold(item.Address, config.AppConfig.USDTContractAddress) {
		return
	}
	if len(item.Topics) < 3 {
		return
	}

	// Ekstrak from_address
	fromTopic := item.Topics[1]
	cleanFrom := strings.TrimPrefix(fromTopic, "0x")
	if len(cleanFrom) >= 40 {
		cleanFrom = cleanFrom[len(cleanFrom)-40:]
	}
	fromAddress := "0x" + cleanFrom

	// Ekstrak blockNumber dan logIndex
	blockNum, _ := strconv.ParseUint(strings.TrimPrefix(item.BlockNumber, "0x"), 16, 64)
	logIndex, _ := strconv.ParseUint(strings.TrimPrefix(item.LogIndex, "0x"), 16, 64)
	txHash := strings.ToLower(item.TransactionHash)
	txID := fmt.Sprintf("%s_%d", txHash, logIndex)

	// Deduplikasi: Cek apakah tx_id sudah pernah dicatat di orders atau unmatched
	var orderExists int
	_ = p.db.QueryRow("SELECT COUNT(*) FROM orders WHERE tx_id = ?", txID).Scan(&orderExists)
	if orderExists > 0 {
		return
	}

	var unExists int
	_ = p.db.QueryRow("SELECT COUNT(*) FROM unmatched_transactions WHERE tx_id = ?", txID).Scan(&unExists)
	if unExists > 0 {
		return
	}

	// Parse rawWei uint256
	cleanData := strings.TrimPrefix(item.Data, "0x")
	rawWei := new(big.Int)
	if _, ok := rawWei.SetString(cleanData, 16); !ok || rawWei.Sign() <= 0 {
		return // Abaikan 0-value transfer poisoning
	}

	// DivMod dengan 10^15 (18 desimal USDT - 3 desimal milli)
	divisor := big.NewInt(1_000_000_000_000_000)
	quotient := new(big.Int)
	remainder := new(big.Int)
	quotient.DivMod(rawWei, divisor, remainder)

	txMilli := quotient.Int64()
	isExactMilli := remainder.Sign() == 0

	amountStr := formatWeiToUSDT(rawWei)

	// Auto-Match hanya jika txMilli > 0 dan remainder == 0 (tanpa sub-milli dust)
	if isExactMilli && txMilli > 0 {
		var matchedOrderID string
		err := p.db.QueryRow(`
			SELECT id FROM orders 
			WHERE currency = 'USDT' 
			  AND total_amount_milli = ? 
			  AND status = 'PENDING'
			ORDER BY created_at ASC LIMIT 1
		`, txMilli).Scan(&matchedOrderID)

		if err == nil && matchedOrderID != "" {
			nowMs := time.Now().UnixMilli()
			_, updateErr := p.db.Exec(`
				UPDATE orders 
				SET status = 'PAID', tx_id = ?, tx_hash = ?, from_address = ?, block_number = ?, paid_at = ?
				WHERE id = ?
			`, txID, txHash, fromAddress, blockNum, nowMs, matchedOrderID)

			if updateErr == nil {
				log.Printf("[Auto-Match Success] Order %s lunas! Tx: %s (%s USDT)", matchedOrderID, txHash, amountStr)
				go func(ordID string) {
					_ = p.webhookSvc.DispatchOrderWebhook(p.db, ordID, false)
				}(matchedOrderID)
				return
			}
		}
	}

	// Masuk ke unmatched_transactions jika tidak match atau isExactMilli == false
	_, insErr := p.db.Exec(`
		INSERT INTO unmatched_transactions (
			tx_id, tx_hash, from_address, currency, amount_str, amount_milli, raw_wei, block_number, tx_time, resolved_order_id
		) VALUES (?, ?, ?, 'USDT', ?, ?, ?, ?, ?, '')
	`, txID, txHash, fromAddress, amountStr, txMilli, rawWei.String(), blockNum, time.Now().UnixMilli())

	if insErr == nil {
		log.Printf("[Unmatched Mutation] Terdeteksi mutasi USDT tak dikenal: %s USDT (Tx: %s, From: %s)", amountStr, txHash, fromAddress)
	}
}

func (p *PollerService) TriggerOnDemandCheck(ctx context.Context) (didScan bool, nextSeconds int) {
	p.mu.Lock()
	minInterval := time.Duration(config.AppConfig.MinRPCPollIntervalSec) * time.Second
	sinceLast := time.Since(p.lastPollTime)
	p.mu.Unlock()

	if sinceLast >= minInterval {
		_ = p.RunScanCycle(ctx)
		return true, 0
	}

	remaining := int((minInterval - sinceLast).Seconds())
	if remaining < 1 {
		remaining = 1
	}
	return false, remaining
}

func formatWeiToUSDT(wei *big.Int) string {
	dec := 18
	weiStr := wei.String()
	if len(weiStr) <= dec {
		padding := strings.Repeat("0", dec-len(weiStr)+1)
		weiStr = padding + weiStr
	}
	intPart := weiStr[:len(weiStr)-dec]
	if intPart == "" {
		intPart = "0"
	}
	fracPart := strings.TrimRight(weiStr[len(weiStr)-dec:], "0")
	if fracPart == "" {
		return intPart
	}
	if len(fracPart) > 6 {
		fracPart = fracPart[:6]
	}
	return intPart + "." + fracPart
}
