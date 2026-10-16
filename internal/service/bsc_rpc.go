package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"paymentg-bsc/internal/config"
)

type RPCLogItem struct {
	Address          string   `json:"address"`
	Topics           []string `json:"topics"`
	Data             string   `json:"data"`
	BlockNumber      string   `json:"blockNumber"`
	TransactionHash  string   `json:"transactionHash"`
	TransactionIndex string   `json:"transactionIndex"`
	BlockHash        string   `json:"blockHash"`
	LogIndex         string   `json:"logIndex"`
	Removed          bool     `json:"removed"`
}

type jsonRPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type MultiRPCClient struct {
	mu         sync.Mutex
	httpClient *http.Client
	activeIdx  int
}

func NewMultiRPCClient() *MultiRPCClient {
	return &MultiRPCClient{
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		activeIdx: 0,
	}
}

func (c *MultiRPCClient) GetActiveRPC() string {
	urls := config.AppConfig.GetRPCURLs()
	if len(urls) == 0 {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeIdx >= len(urls) {
		c.activeIdx = 0
	}
	return urls[c.activeIdx]
}

func (c *MultiRPCClient) callRPCWithFailover(ctx context.Context, method string, params []interface{}) (json.RawMessage, string, error) {
	urls := config.AppConfig.GetRPCURLs()
	if len(urls) == 0 {
		return nil, "", fmt.Errorf("no RPC URLs configured")
	}

	totalURLs := len(urls)
	c.mu.Lock()
	startIdx := c.activeIdx
	c.mu.Unlock()

	var lastErr error

	for i := 0; i < totalURLs; i++ {
		currentIdx := (startIdx + i) % totalURLs
		currentURL := urls[currentIdx]

		reqBody, err := json.Marshal(jsonRPCRequest{
			JSONRPC: "2.0",
			Method:  method,
			Params:  params,
			ID:      1,
		})
		if err != nil {
			return nil, "", fmt.Errorf("failed to marshal jsonrpc request: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, currentURL, bytes.NewReader(reqBody))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("rpc node %s error: %w", currentURL, err)
			continue
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()

		if err != nil {
			lastErr = fmt.Errorf("failed to read body from %s: %w", currentURL, err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("rpc node %s returned status %d: %s", currentURL, resp.StatusCode, string(bodyBytes))
			continue
		}

		var rpcResp jsonRPCResponse
		if err := json.Unmarshal(bodyBytes, &rpcResp); err != nil {
			lastErr = fmt.Errorf("failed to parse jsonrpc response from %s: %w", currentURL, err)
			continue
		}

		if rpcResp.Error != nil {
			lastErr = fmt.Errorf("rpc node %s returned jsonrpc error (%d): %s", currentURL, rpcResp.Error.Code, rpcResp.Error.Message)
			continue
		}

		// Sukses! Set node ini sebagai active index
		c.mu.Lock()
		c.activeIdx = currentIdx
		c.mu.Unlock()

		return rpcResp.Result, currentURL, nil
	}

	return nil, "", fmt.Errorf("all RPC nodes failed, last error: %v", lastErr)
}

func (c *MultiRPCClient) GetLatestBlockNumber(ctx context.Context) (uint64, string, error) {
	rawResult, activeURL, err := c.callRPCWithFailover(ctx, "eth_blockNumber", []interface{}{})
	if err != nil {
		return 0, "", err
	}

	var hexBlock string
	if err := json.Unmarshal(rawResult, &hexBlock); err != nil {
		return 0, activeURL, fmt.Errorf("failed to unmarshal eth_blockNumber result: %w", err)
	}

	hexClean := strings.TrimPrefix(hexBlock, "0x")
	blockNum, err := strconv.ParseUint(hexClean, 16, 64)
	if err != nil {
		return 0, activeURL, fmt.Errorf("failed to parse hex block '%s': %w", hexBlock, err)
	}

	return blockNum, activeURL, nil
}

func (c *MultiRPCClient) FormatReceiverTopic(walletAddress string) string {
	clean := strings.ToLower(strings.TrimSpace(walletAddress))
	clean = strings.TrimPrefix(clean, "0x")
	if len(clean) > 40 {
		clean = clean[len(clean)-40:]
	}
	padding := strings.Repeat("0", 64-len(clean))
	return "0x" + padding + clean
}

func (c *MultiRPCClient) GetLogs(ctx context.Context, fromBlock, toBlock uint64, receiverWallet string) ([]RPCLogItem, error) {
	fromHex := fmt.Sprintf("0x%x", fromBlock)
	toHex := fmt.Sprintf("0x%x", toBlock)

	receiverTopic := c.FormatReceiverTopic(receiverWallet)
	transferTopic := "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

	filterParam := map[string]interface{}{
		"fromBlock": fromHex,
		"toBlock":   toHex,
		"address":   config.AppConfig.USDTContractAddress,
		"topics": []interface{}{
			transferTopic,
			nil, // from address (siapapun pengirimnya)
			receiverTopic,
		},
	}

	rawResult, _, err := c.callRPCWithFailover(ctx, "eth_getLogs", []interface{}{filterParam})
	if err != nil {
		return nil, err
	}

	var logs []RPCLogItem
	if err := json.Unmarshal(rawResult, &logs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal eth_getLogs result: %w", err)
	}

	return logs, nil
}

func (c *MultiRPCClient) TestRPCConnection(ctx context.Context) (uint64, string, int64, error) {
	start := time.Now()
	blockNum, activeRPC, err := c.GetLatestBlockNumber(ctx)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return 0, "", latency, err
	}
	return blockNum, activeRPC, latency, nil
}
