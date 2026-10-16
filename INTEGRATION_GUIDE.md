# PANDUAN INTEGRASI MERCHANT: PaymentG (USDT BEP-20)

Panduan ini ditujukan bagi developer maupun AI coding agent untuk mengintegrasikan gateway pembayaran **PaymentG (USDT BEP-20 / BNB Smart Chain)** ke dalam website toko online, aplikasi e-commerce, atau sistem berlangganan Anda.

---

## 1. Konsep Utama & Jalur Endpoint

PaymentG menyediakan dua mode yang dapat digunakan:
- **Sandbox Mode (Pengujian):** Menggunakan basis data terisolasi tanpa saldo kripto riil. Anda dapat mensimulasikan pembayaran 1-klik di UI `/sandbox/`.
- **Live Mode (Produksi):** Memantau mutasi on-chain BSC secara langsung dan otomatis.

| Endpoint | Jalur Live | Jalur Sandbox | Method | Header Wajib |
| :--- | :--- | :--- | :--- | :--- |
| **Buat Tagihan Baru** | `/api/orders` | `/api/sandbox/orders` | `POST` | `X-API-Key: <api_key>` |
| **Cek Status Tagihan** | `/api/orders/:id` | `/api/sandbox/orders/:id` | `GET` | Opsional |
| **Tombol "Saya Sudah Bayar"** | `/api/orders/:id/check` | `/api/sandbox/orders/:id/check` | `POST` | Bebas / Publik |
| **Tampilkan Gambar QR** | `/api/orders/:id/qr.png` | `/api/sandbox/orders/:id/qr.png` | `GET` | Publik (Tanpa Auth) |
| **Batalkan Tagihan** | `/api/orders/:id/cancel` | `/api/sandbox/orders/:id/cancel` | `POST` | `X-API-Key: <api_key>` |

---

## 2. Membuat Tagihan (`POST /api/orders`)

Kirimkan request HTTP `POST` saat pembeli memilih metode pembayaran USDT (BEP-20):

### Request Headers & Body:
```http
POST /api/orders HTTP/1.1
Host: localhost:3400
Content-Type: application/json
X-API-Key: ak_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx

{
  "reference_id": "INV-2026-001",
  "amount": 10.50
}
```

### Response Sukses (`HTTP 201 Created`):
```json
{
  "success": true,
  "data": {
    "order_id": "ord_9xK2mP7vLq1A",
    "reference_id": "INV-2026-001",
    "network": "BSC (BEP-20)",
    "chain_id": 56,
    "currency": "USDT",
    "token_contract": "0x55d398326f99059fF775485246999027B3197955",
    "wallet_address": "0xYourPersonalBscWalletAddressHere",
    "original_amount": 10.5,
    "unique_code": 14,
    "total_amount": 10.514,
    "total_amount_str": "10.514",
    "cex_warning": "PENTING: Hanya kirim USDT melalui jaringan BNB Smart Chain (BEP-20). Jika mengirim dari Exchange (Indodax/Tokocrypto/Bybit/Binance), pastikan Jumlah Bersih Diterima (Receive Amount) adalah TEPAT 10.514 USDT setelah dikurangi biaya penarikan.",
    "status": "PENDING",
    "qr_url": "http://localhost:3400/api/orders/ord_9xK2mP7vLq1A/qr.png",
    "expires_at": "2026-10-16T15:15:00Z",
    "expires_in_seconds": 900
  }
}
```

> [!IMPORTANT]
> **Idempotensi:** Jika toko Anda memanggil endpoint dengan `reference_id` yang sama dan pesanan tersebut masih `PENDING`, PaymentG akan mengembalikan tagihan yang ada tanpa membuat kode unik baru.

---

## 3. Menangani Webhook Notifikasi Pelunasan (`payment.success`)

Ketika transaksi terkonfirmasi on-chain pada BNB Smart Chain (atau disimulasikan di Sandbox), PaymentG akan mengirimkan HTTP `POST` ke `webhook_url` yang terdaftar pada toko Anda.

### Headers Webhook yang Dikirim:
- `Content-Type: application/json`
- `X-Webhook-Event: payment.success`
- `X-Webhook-Timestamp: <unix_timestamp_detik>`
- `X-Webhook-Signature: <hex_hmac_sha256>`

### Payload Webhook:
```json
{
  "event": "payment.success",
  "is_sandbox": false,
  "order_id": "ord_9xK2mP7vLq1A",
  "reference_id": "INV-2026-001",
  "network": "BSC (BEP-20)",
  "currency": "USDT",
  "original_amount": 10.5,
  "unique_code": 14,
  "total_amount": 10.514,
  "total_amount_str": "10.514",
  "wallet_address": "0xYourPersonalBscWalletAddressHere",
  "from_address": "0xBuyerSenderWalletAddressHere",
  "tx_hash": "0x8a9f7c6d5e4b3a2f1e0d9c8b7a6f5e4d3c2b1a0f9e8d7c6b5a4f3e2d1c0b9a8f",
  "block_number": 43120950,
  "paid_at": "2026-10-16T15:03:12Z",
  "timestamp": 1760616192
}
```

---

## 4. Contoh Kode Verifikasi Signature Webhook

Signature dihitung dengan:
$$\text{Signature} = \text{hex}\Big(\text{HMAC-SHA256}\big(\text{Raw Body JSON Bytes},\, \text{API Key Toko}\big)\Big)$$

### A. PHP (Laravel / Native)
```php
<?php
$apiKey = "ak_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"; // API Key Toko Anda
$rawBody = file_get_contents('php://input');
$signatureHeader = $_SERVER['HTTP_X_WEBHOOK_SIGNATURE'] ?? '';

$expectedSignature = hash_hmac('sha256', $rawBody, $apiKey);

if (!hash_equals($expectedSignature, $signatureHeader)) {
    http_response_code(401);
    exit('Invalid Webhook Signature');
}

$payload = json_decode($rawBody, true);
if ($payload['event'] === 'payment.success') {
    $refID = $payload['reference_id'];
    $txHash = $payload['tx_hash'];
    // Update status pesanan di database toko Anda menjadi SUKSES / LUNAS
    // misal: Order::where('invoice_number', $refID)->update(['status' => 'PAID']);
}

http_response_code(200);
echo json_encode(['received' => true]);
```

### B. Node.js (Express.js)
```javascript
const express = require('express');
const crypto = require('crypto');
const app = express();

// PENTING: Gunakan raw body parser untuk verifikasi HMAC
app.post('/api/webhook', express.raw({ type: 'application/json' }), (req, res) => {
  const apiKey = "ak_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx";
  const receivedSig = req.headers['x-webhook-signature'];
  const rawBody = req.body;

  const expectedSig = crypto
    .createHmac('sha256', apiKey)
    .update(rawBody)
    .digest('hex');

  if (receivedSig !== expectedSig) {
    return res.status(401).send('Invalid signature');
  }

  const payload = JSON.parse(rawBody.toString('utf-8'));
  if (payload.event === 'payment.success') {
    console.log(`Pesanan ${payload.reference_id} lunas via tx ${payload.tx_hash}`);
    // Proses pelunasan pesanan Anda
  }

  res.status(200).json({ received: true });
});

app.listen(3000);
```

### C. Python (FastAPI / Flask)
```python
import hmac
import hashlib
from fastapi import FastAPI, Request, HTTPException

app = FastAPI()
API_KEY = "ak_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

@app.post("/api/webhook")
async def handle_webhook(request: Request):
    raw_body = await request.body()
    received_sig = request.headers.get("X-Webhook-Signature", "")

    expected_sig = hmac.new(API_KEY.encode(), raw_body, hashlib.sha256).hexdigest()

    if not hmac.compare_digest(expected_sig, received_sig):
        raise HTTPException(status_code=401, detail="Invalid signature")

    payload = await request.json()
    if payload.get("event") == "payment.success":
        ref_id = payload.get("reference_id")
        # Update database toko Anda
        print(f"Pesanan {ref_id} telah lunas!")

    return {"received": True}
```

### D. Golang
```go
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net/http"
)

func webhookHandler(w http.ResponseWriter, r *http.Request) {
	apiKey := "ak_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	body, _ := io.ReadAll(r.Body)
	sigHeader := r.Header.Get("X-Webhook-Signature")

	mac := hmac.New(sha256.New, []byte(apiKey))
	mac.Write(body)
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if subtle.ConstantTimeCompare([]byte(expectedSig), []byte(sigHeader)) != 1 {
		http.Error(w, "Invalid signature", http.StatusUnauthorized)
		return
	}

	// Payload valid, proses pesanan...
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"received":true}`))
}
```

---

## 5. Rekomendasi Desain UI Checkout Toko (Peringatan Khusus CEX)

Tampilkan komponen berikut di halaman checkout toko Anda agar pembeli tidak keliru mengirim nominal atau jaringan:

```html
<div class="crypto-checkout-card" style="border: 2px solid #F0B90B; padding: 20px; border-radius: 12px; background: #181A20; color: #FFF; font-family: sans-serif;">
  <div style="text-align: center;">
    <h3 style="color: #F0B90B; margin-bottom: 5px;">Pembayaran USDT BEP-20 (BNB Smart Chain)</h3>
    <p style="font-size: 13px; color: #848E9C;">Scan QR atau kirim nominal persis ke alamat di bawah ini:</p>
    
    <!-- Gambar QR dari PaymentG -->
    <img src="http://localhost:3400/api/orders/ord_xxxx/qr.png" alt="QR Code" width="220" style="border-radius: 8px; margin: 15px auto; display: block;" />
    
    <div style="background: #0B0E11; padding: 12px; border-radius: 8px; margin-bottom: 12px;">
      <span style="font-size: 12px; color: #848E9C;">Jumlah yang WAJIB Diterima:</span>
      <div style="font-size: 24px; font-weight: bold; color: #F0B90B;">10.514 USDT</div>
      <small style="color: #F0B90B;">(Termasuk kode unik mikro verifikasi otomatis)</small>
    </div>

    <!-- Peringatan Potongan Biaya Exchange (CEX Warning Box) -->
    <div style="background: #2E1B00; border: 1px solid #F0B90B; border-radius: 8px; padding: 12px; text-align: left; font-size: 12px; margin-bottom: 15px;">
      <strong style="color: #F0B90B;">⚠️ PERINGATAN PENARIKAN EXCHANGE (Indodax, Tokocrypto, Bybit, Binance):</strong>
      <p style="margin: 5px 0 0 0; color: #EAECEF;">
        Bursa mengenakan biaya penarikan (misal 0.3 atau 0.8 USDT). Pastikan <strong>"Jumlah Bersih Diterima" (Receive Amount)</strong> adalah tepat <strong>10.514 USDT</strong>, bukan jumlah yang Anda ketik di kolom kirim.
      </p>
    </div>

    <button onclick="checkPaymentStatus()" style="background: #F0B90B; color: #000; border: none; padding: 12px 24px; font-weight: bold; border-radius: 8px; cursor: pointer; width: 100%;">
      Saya Sudah Bayar
    </button>
  </div>
</div>
```
