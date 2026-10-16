package service

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/skip2/go-qrcode"
)

type QRService struct{}

func NewQRService() *QRService {
	return &QRService{}
}

func (s *QRService) GetQRPath(orderID string, isSandbox bool) string {
	if isSandbox {
		return filepath.Join("data", "sandbox", "qr", fmt.Sprintf("%s.png", orderID))
	}
	return filepath.Join("data", "qr", fmt.Sprintf("%s.png", orderID))
}

func (s *QRService) GenerateQRCode(orderID, walletAddress string, isSandbox bool) (string, error) {
	filePath := s.GetQRPath(orderID, isSandbox)
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create qr directory: %w", err)
	}

	// Buat QR code 320x320 PNG
	err := qrcode.WriteFile(walletAddress, qrcode.Medium, 320, filePath)
	if err != nil {
		return "", fmt.Errorf("failed to write qr code file: %w", err)
	}

	return filePath, nil
}

func (s *QRService) EnsureQRCode(orderID, walletAddress string, isSandbox bool) (string, error) {
	filePath := s.GetQRPath(orderID, isSandbox)
	info, err := os.Stat(filePath)
	if err == nil && info.Size() > 0 {
		return filePath, nil
	}

	// Auto-Heal jika file hilang atau kosong
	return s.GenerateQRCode(orderID, walletAddress, isSandbox)
}
