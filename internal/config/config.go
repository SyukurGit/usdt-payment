package config

import (
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/joho/godotenv"
)

type Config struct {
	mu sync.RWMutex

	Port                       string
	GinMode                    string
	AdminPIN                   string
	AdminKey                   string
	NetworkName                string
	ChainID                    int64
	ReceiverWalletAddress      string
	USDTContractAddress        string
	USDTDecimals               int
	BSCRPCURLs                 []string
	DefaultCurrency            string
	DefaultExpiryMinutes       int
	CooldownMinutes            int
	RequiredConfirmations      uint64
	MinRPCPollIntervalSec      int
	BackgroundSweepIntervalSec int
	MaxBlockRangePerQuery      uint64
}

var AppConfig *Config

func LoadConfig() (*Config, error) {
	_ = godotenv.Load() // Abaikan jika file .env tidak ditemukan, fallback ke os.Getenv

	port := getEnv("PORT", "3400")
	ginMode := getEnv("GIN_MODE", "release")
	adminPin := getEnv("ADMIN_PIN", "2207")
	adminKey := getEnv("ADMIN_KEY", "pg_bsc_admin_secret_key_change_me")
	networkName := getEnv("NETWORK_NAME", "BSC (BEP-20)")
	chainID, _ := strconv.ParseInt(getEnv("CHAIN_ID", "56"), 10, 64)
	receiverWallet := strings.TrimSpace(getEnv("RECEIVER_WALLET_ADDRESS", "0x71C67Ed3484A222ab20E083d9204E646A803D0A1"))
	usdtContract := strings.TrimSpace(getEnv("USDT_CONTRACT_ADDRESS", "0x55d398326f99059fF775485246999027B3197955"))
	usdtDecimals, _ := strconv.Atoi(getEnv("USDT_DECIMALS", "18"))
	if usdtDecimals == 0 {
		usdtDecimals = 18
	}

	rpcRaw := getEnv("BSC_RPC_URLS", "https://bsc-rpc.publicnode.com,https://1rpc.io/bnb,https://bsc.meowrpc.com,https://bsc-dataseed.binance.org,https://bsc-dataseed1.defibit.io,https://bsc-dataseed1.ninicoin.io")
	rpcURLs := parseCommaSeparated(rpcRaw)

	defaultCurrency := getEnv("DEFAULT_CURRENCY", "USDT")
	expiryMin, _ := strconv.Atoi(getEnv("DEFAULT_EXPIRY_MINUTES", "15"))
	if expiryMin <= 0 {
		expiryMin = 15
	}
	cooldownMin, _ := strconv.Atoi(getEnv("COOLDOWN_MINUTES", "60"))
	if cooldownMin <= 0 {
		cooldownMin = 60
	}
	reqConf, _ := strconv.ParseUint(getEnv("REQUIRED_CONFIRMATIONS", "2"), 10, 64)
	if reqConf == 0 {
		reqConf = 2
	}
	minPollSec, _ := strconv.Atoi(getEnv("MIN_RPC_POLL_INTERVAL_SEC", "4"))
	if minPollSec <= 0 {
		minPollSec = 4
	}
	sweepIntervalSec, _ := strconv.Atoi(getEnv("BACKGROUND_SWEEP_INTERVAL_SEC", "6"))
	if sweepIntervalSec <= 0 {
		sweepIntervalSec = 6
	}
	maxBlockRange, _ := strconv.ParseUint(getEnv("MAX_BLOCK_RANGE_PER_QUERY", "400"), 10, 64)
	if maxBlockRange == 0 {
		maxBlockRange = 400
	}

	cfg := &Config{
		Port:                       port,
		GinMode:                    ginMode,
		AdminPIN:                   adminPin,
		AdminKey:                   adminKey,
		NetworkName:                networkName,
		ChainID:                    chainID,
		ReceiverWalletAddress:      receiverWallet,
		USDTContractAddress:        usdtContract,
		USDTDecimals:               usdtDecimals,
		BSCRPCURLs:                 rpcURLs,
		DefaultCurrency:            defaultCurrency,
		DefaultExpiryMinutes:       expiryMin,
		CooldownMinutes:            cooldownMin,
		RequiredConfirmations:      reqConf,
		MinRPCPollIntervalSec:      minPollSec,
		BackgroundSweepIntervalSec: sweepIntervalSec,
		MaxBlockRangePerQuery:      maxBlockRange,
	}

	AppConfig = cfg
	return cfg, nil
}

func (c *Config) GetReceiverWallet() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ReceiverWalletAddress
}

func (c *Config) SetReceiverWallet(addr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ReceiverWalletAddress = strings.TrimSpace(addr)
}

func (c *Config) GetRPCURLs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	copied := make([]string, len(c.BSCRPCURLs))
	copy(copied, c.BSCRPCURLs)
	return copied
}

func (c *Config) SetRPCURLs(urls []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.BSCRPCURLs = urls
}

func (c *Config) GetRequiredConfirmations() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.RequiredConfirmations
}

func (c *Config) SetRequiredConfirmations(conf uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.RequiredConfirmations = conf
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val)
	}
	return defaultVal
}

func parseCommaSeparated(raw string) []string {
	parts := strings.Split(raw, ",")
	var result []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
