# MASTER SPECIFICATION: PaymentG — USDT BEP-20 (BSC On-Chain Edition)

> **INSTRUKSI MUTLAK UNTUK AI AGENT CLI:**
> Bangun aplikasi **PaymentG (USDT BEP-20 On-Chain Edition)** secara lengkap, utuh, dan siap produksi (**Zero Placeholder, Zero TODO, Zero Mock pada jalur Live**). Ikuti seluruh arsitektur, skema database SQLite, logika JSON-RPC `eth_getLogs`, konversi `big.Int`, keamanan anti-manipulasi, serta antarmuka `/dashboard` dan `/sandbox` di dalam spesifikasi ini secara **eksak**.

---

## 1. Gambaran Umum & Prinsip Keamanan Utama

**PaymentG (USDT BEP-20 Edition)** adalah *self-hosted Web3 crypto payment gateway* mandiri berkinerja tinggi (Go + Gin + Pure-Go SQLite) yang memantau mutasi masuk token **USDT (Binance-Peg BSC-USD)** di jaringan **BNB Smart Chain (BEP-20)** secara langsung melalui node **EVM JSON-RPC** tanpa memerlukan API Key pihak ketiga dan **tanpa menyimpan Private Key (100% Non-Custodial / Watch-Only Wallet)**.

### Tech Stack Wajib:
- **Bahasa:** Go `1.22+` dengan `CGO_ENABLED=0` (Single Binary, RAM < 25 MB).
- **HTTP Framework:** `github.com/gin-gonic/gin`
- **Database:** `modernc.org/sqlite` (Driver name: `"sqlite"`, mode WAL).
- **QR Code Generator:** `github.com/skip2/go-qrcode`
- **Environment Loader:** `github.com/joho/godotenv`
- **Frontend UI (`/dashboard/` & `/sandbox/`):** HTML5 + TailwindCSS (CDN) + Vanilla JS dengan proteksi XSS (`escapeHtml`).

---

## 2. Struktur Direktori Proyek

```text
paymentg-bsc/
├── data/
│   ├── payment.db               # Auto-generated: SQLite Production (Transaksi On-Chain BSC Riil)
│   ├── qr/                      # Auto-generated: Cache gambar QR PNG Live
│   └── sandbox/
│       ├── payment.db           # Auto-generated: SQLite Sandbox (Testing Terisolasi 100%)
│       └── qr/                  # Auto-generated: Cache gambar QR PNG Sandbox
├── dashboard/
│   ├── index.html               # UI Dashboard Live (/dashboard/) + Manual Match Unmatched Tx
│   └── sandbox.html             # UI Simulator Sandbox (/sandbox/) + Simulasi Bayar 1-Klik
├── internal/
│   ├── config/
│   │   └── config.go            # Parser .env & runtime config
│   ├── database/
│   │   └── sqlite.go            # Koneksi SQLite WAL (Live & Sandbox) + Sync tabel apps dwiarah
│   ├── middleware/
│   │   ├── auth.go              # Guard X-Admin-Key (Brute-force lock) & X-API-Key
│   │   └── cors.go              # Middleware CORS
│   ├── service/
│   │   ├── bsc_rpc.go           # Multi-RPC EVM Client (eth_blockNumber, eth_getLogs, Auto-Failover)
│   │   ├── order.go             # Tier Kode Unik Mikro Adaptif, Matching BigInt, & Manual Match
│   │   ├── poller.go            # Smart Block-Cursor Poller, 2-Block Finality, & On-Demand Rate-Gate
│   │   ├── qr.go                # Generator PNG QR 320x320 dengan Auto-Heal On-the-Fly
│   │   └── webhook.go           # Webhook Dispatcher, In-Flight Lock, HMAC-SHA256 + Timestamp Guard
│   └── handler/
│       ├── admin.go             # Handler PIN auth, stats, apps, config wallet/RPC, & manual approve
│       ├── order.go             # Handler Client API (Create, Get, Check, Cancel, Serve QR)
│       └── sandbox.go           # Handler Simulator Sandbox (1-Click Pay, Create Demo, Credentials)
├── .env
├── .env.example
├── go.mod
├── main.go                      # Entry point, HTTP server, background workers, & Graceful Shutdown
├── RESULT.md                    # Dokumentasi hasil proyek lengkap
└── INTEGRATION_GUIDE.md         # Panduan integrasi mandiri untuk developer / AI agent web toko
```

---

## 3. Spesifikasi File `.env.example`

```ini
# Server Configuration
PORT=3400
GIN_MODE=release

# Keamanan Admin & Dashboard
ADMIN_PIN=2207
ADMIN_KEY=pg_bsc_admin_secret_key_change_me

# Konfigurasi Jaringan & Dompet Penerima (WATCH-ONLY PUBLIC ADDRESS — JANGAN MASUKKAN PRIVATE KEY!)
NETWORK_NAME=BSC (BEP-20)
CHAIN_ID=56
RECEIVER_WALLET_ADDRESS=0xYourPersonalBscWalletAddressHere
USDT_CONTRACT_ADDRESS=0x55d398326f99059fF775485246999027B3197955
USDT_DECIMALS=18

# Daftar Public RPC BSC (Dipisahkan koma untuk Auto-Failover berurutan)
BSC_RPC_URLS=[https://bsc-dataseed.binance.org](https://bsc-dataseed.binance.org),[https://bsc-rpc.publicnode.com](https://bsc-rpc.publicnode.com),[https://bsc-dataseed1.defibit.io](https://bsc-dataseed1.defibit.io),[https://bsc-dataseed1.ninicoin.io](https://bsc-dataseed1.ninicoin.io),[https://1rpc.io/bnb](https://1rpc.io/bnb)

# Pengaturan Tagihan, Finality & Poller
DEFAULT_CURRENCY=USDT
DEFAULT_EXPIRY_MINUTES=15
COOLDOWN_MINUTES=60
REQUIRED_CONFIRMATIONS=2
MIN_RPC_POLL_INTERVAL_SEC=4
BACKGROUND_SWEEP_INTERVAL_SEC=6
MAX_BLOCK_RANGE_PER_QUERY=400
```

---

## 4. Skema Database SQLite (`data/payment.db` & `data/sandbox/payment.db`)

Setiap koneksi database (`LiveDB` dan `SandboxDB`) **WAJIB** menggunakan parameter:
`file:<path>?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)`
dan `db.SetMaxOpenConns(1)`.

```sql
-- 1. Konfigurasi Runtime (Dapat diubah via Dashboard Admin tanpa restart)
CREATE TABLE IF NOT EXISTS system_config (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 2. Daftar Toko / Aplikasi Klien (Disinkronkan otomatis antara LiveDB & SandboxDB)
CREATE TABLE IF NOT EXISTS apps (
    id TEXT PRIMARY KEY,                  -- Format: "app_" + 12 random chars
    name TEXT NOT NULL,                   -- Nama toko
    api_key TEXT NOT NULL UNIQUE,         -- Format: "ak_" + 32 random hex chars
    webhook_url TEXT NOT NULL,            -- URL tujuan webhook toko
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 3. Tabel Pesanan / Tagihan (Orders)
CREATE TABLE IF NOT EXISTS orders (
    id TEXT PRIMARY KEY,                  -- Format: "ord_" + 12 random chars
    app_id TEXT NOT NULL,                 -- FK ke apps.id
    reference_id TEXT NOT NULL,           -- ID Invoice dari toko (misal: "INV-2026-001")
    network TEXT NOT NULL DEFAULT 'BSC',  -- Jaringan ("BSC")
    currency TEXT NOT NULL DEFAULT 'USDT',-- Mata uang ("USDT")
    wallet_address TEXT NOT NULL,         -- Alamat wallet 0x... penerima saat order dibuat
    original_amount REAL NOT NULL,        -- Nominal dasar (misal: 0.8)
    original_amount_milli INTEGER NOT NULL, -- Nominal dasar x 1000 (misal: 800)
    unique_code INTEGER NOT NULL,         -- Kode unik mikro (misal: 14 -> +0.014 USDT)
    total_amount REAL NOT NULL,           -- Total float (0.814)
    total_amount_str TEXT NOT NULL,       -- String eksak 3 desimal ("0.814")
    total_amount_milli INTEGER NOT NULL,  -- Integer eksak untuk matching (814)
    status TEXT NOT NULL DEFAULT 'PENDING', -- PENDING | PAID | EXPIRED | CANCELLED
    tx_id TEXT UNIQUE,                    -- Kunci deduplikasi unik: "<tx_hash>_<log_index>"
    tx_hash TEXT DEFAULT '',              -- Hash transaksi BSC (0x...)
    from_address TEXT DEFAULT '',         -- Alamat wallet pengirim (0x...)
    block_number INTEGER DEFAULT 0,       -- Nomor blok saat transaksi masuk
    qr_path TEXT NOT NULL,                -- Lokasi file PNG QR di disk
    created_at INTEGER NOT NULL,          -- Unix timestamp (ms)
    expires_at INTEGER NOT NULL,          -- Unix timestamp (ms)
    cooldown_until INTEGER NOT NULL,      -- Unix timestamp (ms) = expires_at + (COOLDOWN_MINUTES * 60000)
    paid_at INTEGER DEFAULT 0,            -- Unix timestamp (ms) saat lunas
    webhook_delivered INTEGER DEFAULT 0,  -- 0 = belum, 1 = terkirim HTTP 2xx
    webhook_attempts INTEGER DEFAULT 0,   -- Jumlah percobaan kirim webhook
    next_webhook_retry_at INTEGER DEFAULT 0, -- Unix timestamp (ms) jadwal retry berikutnya
    FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_orders_matching 
ON orders(currency, total_amount_milli, status, cooldown_until);

CREATE INDEX IF NOT EXISTS idx_orders_status_created 
ON orders(status, created_at, expires_at);

-- 4. Tabel Mutasi On-Chain Tak Dikenal (Salah Nominal / Terpotong Fee CEX)
CREATE TABLE IF NOT EXISTS unmatched_transactions (
    tx_id TEXT PRIMARY KEY,               -- "<tx_hash>_<log_index>"
    tx_hash TEXT NOT NULL,                -- Hash transaksi BSC (0x...)
    from_address TEXT NOT NULL,           -- Alamat pengirim (0x...)
    currency TEXT NOT NULL DEFAULT 'USDT',
    amount_str TEXT NOT NULL,             -- Contoh: "9.514"
    amount_milli INTEGER NOT NULL,        -- Contoh: 9514
    raw_wei TEXT NOT NULL,                -- Nilai uint256 asli dalam string desimal
    block_number INTEGER NOT NULL,
    tx_time INTEGER NOT NULL,             -- Waktu terdeteksi (ms)
    resolved_order_id TEXT DEFAULT '',    -- Diisi jika di-approve manual oleh Admin ke order tertentu
    recorded_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

---

## 5. Spesifikasi Mesin Inti & Anti-Manipulasi (Deep-Dive)

### 5.1 Alokasi Kode Unik Mikro Adaptif (1-Query + Mutex)
- Validasi input: `amount` minimal adalah `0.01` USDT. Jika `< 0.01`, tolak dengan `HTTP 400 Bad Request`.
- Hitung `original_amount_milli = int64(math.Round(amount * 1000))`.
- Gunakan `sync.Mutex` saat mengalokasikan kode unik agar bebas *race condition*:
  - **Tier 1 (`amount < 5.0` USDT):** Cari slot acak di rentang `1..25` (`+0.001` s.d `+0.025` USDT).
  - **Tier 2 (`5.0 <= amount < 50.0` USDT):** Cari slot acak di rentang `1..50` (`+0.001` s.d `+0.050` USDT).
  - **Tier 3 (`amount >= 50.0` USDT):** Cari slot acak di rentang `1..99` (`+0.001` s.d `+0.099` USDT).
  - **Emergency Expansion:** Jika seluruh slot pada tier di atas sedang aktif/cooldown, perluas rentang pencarian hingga `1..250` agar tidak ada order yang gagal dibuat.
- **Query Cek Slot Terpakai (1 Single SQL Query):**
  Ambil daftar `total_amount_milli` yang sedang dikunci di database:
  ```sql
  SELECT total_amount_milli FROM orders
  WHERE currency = ?
    AND total_amount_milli BETWEEN ? AND ?
    AND (
      status = 'PENDING'
      OR (status IN ('PAID', 'EXPIRED', 'CANCELLED') AND cooldown_until > ?)
    )
  ```
  Pilih angka `code` yang menghasilkan `candidate_milli = original_amount_milli + code` yang tidak ada di dalam daftar tersebut.

---

### 5.2 Spesifikasi Multi-RPC EVM Client & Filter `eth_getLogs` (`internal/service/bsc_rpc.go`)
1. **Keamanan Alamat Kontrak & Topic:**
   - Kontrak Resmi USDT BEP-20: `0x55d398326f99059fF775485246999027B3197955` (ambil dari runtime config / `.env`, wajib *case-insensitive*).
   - `Transfer(address,address,uint256)` Topic0 Hash Tetap:
     `0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef`
   - **Format `Topic2` (Alamat Penerima 32-Byte Padded):**
     Ambil `RECEIVER_WALLET_ADDRESS` (misal `0xAbC123...`, wajib valid 42 karakter hex diawali `0x`), buang `0x`, ubah ke huruf kecil (*lowercase*), lalu tambahkan 24 angka nol di depannya menjadi:
     `0x000000000000000000000000<40_hex_chars_wallet_address>` (Total 66 karakter).
2. **Multi-RPC Failover:**
   - Setiap panggilan JSON-RPC (`eth_blockNumber` atau `eth_getLogs`) memiliki timeout HTTP `5 detik` per node.
   - Jika node RPC ke-1 gagal, error HTTP, atau mengembalikan JSON-RPC error, otomatis ulangi request ke node RPC ke-2, ke-3, dan seterusnya dari daftar `BSC_RPC_URLS`.
3. **Request Payload `eth_getLogs`:**
   ```json
   {
     "jsonrpc": "2.0",
     "id": 1,
     "method": "eth_getLogs",
     "params": [{
       "fromBlock": "0x...",
       "toBlock": "0x...",
       "address": "0x55d398326f99059fF775485246999027B3197955",
       "topics": [
         "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
         null,
         "0x000000000000000000000000<lowercase_40_char_receiver_wallet>"
       ]
     }]
   }
   ```
4. **Konversi Eksak `uint256` Hex ke `amount_milli` (Anti Sub-Milli Spoofing):**
   - Untuk setiap item log di dalam `result`:
     - Abaikan jika `log.Removed == true` (berarti terkena *chain reorg*).
     - Verifikasi `strings.EqualFold(log.Address, expectedUSDTContract)`.
     - Ekstrak `from_address` dari `log.Topics[1]` (`"0x" + log.Topics[1][26:]`).
     - Parse `log.Data` (heksadesimal nilai transfer) ke `rawWei := new(big.Int)`.
     - Abaikan jika `rawWei.Sign() <= 0` (abaikan *0-value transfer poisoning*).
     - Hitung pembagian bilangan bulat terhadap $10^{15}$ (`divisor := big.NewInt(1_000_000_000_000_000)` karena $18 \text{ desimal} - 3 \text{ desimal milli} = 15$):
       ```go
       quotient := new(big.Int)
       remainder := new(big.Int)
       quotient.DivMod(rawWei, divisor, remainder)
       txMilli := quotient.Int64()
       isExactMilli := remainder.Sign() == 0
       ```
     - Buat kunci unik transaksi: `txID := fmt.Sprintf("%s_%d", strings.ToLower(log.TransactionHash), logIndexInt)`.
     - **PENTING:** Sebuah transaksi hanya boleh dicocokkan secara otomatis (`Auto-Match`) ke tabel `orders` jika `txMilli > 0` **DAN** `isExactMilli == true` (tidak ada pecahan aneh di bawah 3 desimal). Jika `isExactMilli == false` atau `txMilli` tidak cocok dengan order mana pun, masukkan ke tabel `unmatched_transactions`!

---

### 5.3 Manajemen Cursor Blok, 2-Block Finality, & Smart Poller (`internal/service/poller.go`)
1. **Finality Guard (2 Konfirmasi Blok):**
   - Saat memanggil `eth_blockNumber` dan mendapatkan `latestBlock`, hitung blok aman:
     `safeBlock = latestBlock - REQUIRED_CONFIRMATIONS` (default `latestBlock - 2`).
   - Poller hanya memindai hingga `safeBlock` untuk menjamin transaksi sudah terkonfirmasi minimal 2 blok (~6 detik) dan bebas *reorg*.
2. **Manajemen Cursor `last_scanned_block` (Disimpan di `system_config`):**
   - Poller hanya berjalan aktif jika di `LiveDB` terdapat order berstatus `PENDING` **ATAU** terdapat order berstatus `EXPIRED` yang baru kedaluwarsa dalam **15 menit terakhir** (*Tail-Polling* untuk menangkap transfer CEX yang agak lambat).
   - **Smart Block Jump:** Jika poller baru bangun dari kondisi *Idle* (atau `last_scanned_block == 0`, atau `safeBlock - last_scanned_block > 1200`), lompat langsung ke `fromBlock = safeBlock - 20` (~1 menit ke belakang) agar tidak memindai ribuan blok lama yang tidak relevan.
   - Jika rentang `safeBlock - fromBlock > MAX_BLOCK_RANGE_PER_QUERY` (400 blok), lakukan pemindaian bertahap per 400 blok.
   - Setelah `eth_getLogs` sukses diproses tanpa error, simpan `last_scanned_block = toBlock` ke tabel `system_config`.
3. **On-Demand Trigger Saat Pembeli Klik "Saya Sudah Bayar" (`POST /api/orders/:id/check`):**
   - Jika jarak sejak pemindaian RPC terakhir `>= MIN_RPC_POLL_INTERVAL_SEC` (4 detik), langsung jalankan pemindaian blok terbaru secara sinkron saat itu juga dan kembalikan status terbaru order!
   - Jika `< 4 detik`, kembalikan status dari SQLite beserta `"next_check_in_seconds"`.

---

### 5.4 Fitur "Manual Match / Force Approve" untuk Kasus Potongan Fee CEX
Pembeli yang mengirim dari CEX (Indodax, Tokocrypto, Bybit) terkadang lupa menambahkan *withdrawal fee*, sehingga jumlah yang mendarat di dompet Anda berbeda dari `total_amount_str` (misal tagihan `10.014 USDT`, tapi masuk `9.014 USDT`) dan tercatat di tabel `unmatched_transactions`.
- Sediakan endpoint Admin: `POST /api/unmatched/resolve`
- **Request Body:**
  ```json
  {
    "tx_id": "0xabc123..._0",
    "order_id": "ord_xxxxxxxxxxxx"
  }
  ```
- **Logika Eksekusi Atomik:**
  1. Pastikan `unmatched_transactions` dengan `tx_id` tersebut ada dan `resolved_order_id == ''`.
  2. Pastikan `orders` dengan `order_id` tersebut ada dan belum `PAID`.
  3. Update order menjadi `status = 'PAID'`, isi `tx_id`, `tx_hash`, `from_address`, `block_number`, dan `paid_at = now`.
  4. Tandai `unmatched_transactions.resolved_order_id = order_id`.
  5. Langsung picu pengiriman Webhook (`go SendOrderWebhook(LiveDB, orderID)`)!

---

## 6. Spesifikasi Lengkap Endpoint REST API

Base URL default: `http://localhost:3400`

### A. Client API (Toko Online — Header `X-API-Key`)

| Method | Endpoint Live | Endpoint Sandbox | Fungsi |
| :--- | :--- | :--- | :--- |
| `POST` | `/api/orders` | `/api/sandbox/orders` | Membuat tagihan USDT BEP-20 baru (Idempoten per `reference_id` yang masih `PENDING`) |
| `GET` | `/api/orders/:id` | `/api/sandbox/orders/:id` | Mengambil detail & status terkini pesanan |
| `GET` | `/api/orders/:id/qr.png` | `/api/sandbox/orders/:id/qr.png` | Gambar QR PNG dari `wallet_address` (**Publik tanpa auth**, dengan *Auto-Heal* jika file terhapus) |
| `POST` | `/api/orders/:id/check` | `/api/sandbox/orders/:id/check` | Tombol "Saya Sudah Bayar" (Memicu sinkronisasi RPC instan jika Live) |
| `POST` | `/api/orders/:id/cancel` | `/api/sandbox/orders/:id/cancel` | Membatalkan pesanan yang masih `PENDING` |

#### Contoh Response `POST /api/orders` (`HTTP 201 Created`):
```json
{
  "success": true,
  "data": {
    "order_id": "ord_9xK2mP7vLq1A",
    "reference_id": "INV-2026-005",
    "network": "BSC (BEP-20)",
    "chain_id": 56,
    "currency": "USDT",
    "token_contract": "0x55d398326f99059fF775485246999027B3197955",
    "wallet_address": "0xYourPersonalBscWalletAddressHere",
    "original_amount": 0.8,
    "unique_code": 14,
    "total_amount": 0.814,
    "total_amount_str": "0.814",
    "cex_warning": "PENTING: Hanya kirim USDT melalui jaringan BNB Smart Chain (BEP-20). Jika mengirim dari Exchange (Indodax/Tokocrypto/Bybit/Binance), pastikan Jumlah Bersih Diterima (Receive Amount) adalah TEPAT 0.814 USDT setelah dikurangi biaya penarikan.",
    "status": "PENDING",
    "qr_url": "http://localhost:3400/api/orders/ord_9xK2mP7vLq1A/qr.png",
    "expires_at": "2026-10-16T12:15:00Z",
    "expires_in_seconds": 900
  }
}
```
*(Gunakan `X-Forwarded-Proto` dan `X-Forwarded-Host` agar `qr_url` otomatis menjadi URL absolut HTTPS saat di-deploy di VPS).*

#### Contoh Payload Webhook Saat Lunas (`payment.success`):
```json
{
  "event": "payment.success",
  "is_sandbox": false,
  "order_id": "ord_9xK2mP7vLq1A",
  "reference_id": "INV-2026-005",
  "network": "BSC (BEP-20)",
  "currency": "USDT",
  "original_amount": 0.8,
  "unique_code": 14,
  "total_amount": 0.814,
  "total_amount_str": "0.814",
  "wallet_address": "0xYourPersonalBscWalletAddressHere",
  "from_address": "0xBuyerSenderWalletAddressHere",
  "tx_hash": "0x8a9f7c6d5e4b3a2f1e0d9c8b7a6f5e4d3c2b1a0f9e8d7c6b5a4f3e2d1c0b9a8f",
  "block_number": 43120950,
  "paid_at": "2026-10-16T12:03:12Z",
  "timestamp": 1760616192
}
```
Dikirim dengan headers:
- `Content-Type: application/json`
- `X-Webhook-Event: payment.success`
- `X-Webhook-Timestamp: <unix_epoch_seconds>`
- `X-Webhook-Signature: <hex_hmac_sha256_of_raw_json_body_using_app_api_key>`

---

### B. Admin & Monitoring API (Header `X-Admin-Key`)

| Method | Endpoint | Fungsi |
| :--- | :--- | :--- |
| `POST` | `/api/auth/pin` | Login PIN Dashboard (Dilengkapi anti-brute-force delay 300ms, `ConstantTimeCompare`, & lockout 5 menit setelah 5x gagal) |
| `GET` | `/api/health` | Status server, nomor blok BSC terbaru, RPC aktif, & status poller (Publik) |
| `GET` | `/api/stats` | Statistik Live: Total omset USDT, total `PAID`, `PENDING`, `EXPIRED`, `unmatched`, & nomor blok terakhir |
| `GET` | `/api/orders` | Daftar 100 transaksi Live terbaru (mendukung filter `?status=`) |
| `GET` | `/api/unmatched` | Daftar mutasi masuk di BSC yang belum cocok (`unmatched_transactions`) |
| `POST` | `/api/unmatched/resolve` | **Manual Match / Force Approve:** Memasangkan mutasi `unmatched` ke `order_id` tertentu & memicu webhook |
| `GET` | `/api/apps` | Daftar toko terdaftar |
| `POST` | `/api/apps` | Tambah toko baru ke LiveDB & SandboxDB sekaligus |
| `DELETE` | `/api/apps/:id` | Hapus toko dari LiveDB & SandboxDB |
| `GET` | `/api/config/bsc` | Ambil konfigurasi `wallet_address`, `rpc_urls`, `required_confirmations`, & kontrak USDT |
| `PUT` | `/api/config/bsc` | Update `wallet_address`, `rpc_urls`, dan `required_confirmations` secara runtime tanpa restart |
| `POST` | `/api/config/bsc/test` | Tes koneksi ke node RPC BSC, kembalikan `latest_block`, `active_rpc`, dan latensi (ms) |

---

### C. Sandbox Simulator API (`/sandbox/`)

| Method | Endpoint | Fungsi |
| :--- | :--- | :--- |
| `GET` | `/api/sandbox/credentials` | Ambil API Key & contoh cURL toko demo sandbox |
| `GET` | `/api/sandbox/stats` | Statistik khusus transaksi Sandbox |
| `GET` | `/api/sandbox/orders` | Daftar pesanan di database Sandbox |
| `POST` | `/api/sandbox/orders/create-demo` | Buat tagihan test cepat dari UI `/sandbox/` |
| `POST` | `/api/sandbox/orders/:id/pay` | **Simulasi 1-Klik Lunas On-Chain:** Buat `tx_hash` simulasi (`0xsandbox...`), `from_address` simulasi, ubah status jadi `PAID`, dan langsung tembakkan Webhook ke toko! |

---

## 7. Spesifikasi Antarmuka Web (`/dashboard/` & `/sandbox/`)

1. **`dashboard/index.html` (Dashboard Live — Tema Dark BSC Gold `#F0B90B`):**
   - Modal kunci PIN (`POST /api/auth/pin`) dengan penanganan auto-relogin jika sesi 401.
   - Indikator Status Blok BSC Real-Time di navbar (menampilkan nomor blok BSC saat ini & RPC yang aktif).
   - **Tab 1 (Transaksi Live):** Tabel daftar pesanan lengkap dengan link klik langsung ke `https://bscscan.com/tx/<tx_hash>` dan `https://bscscan.com/address/<from_address>`.
   - **Tab 2 (Mutasi Tak Cocok / Unmatched + Tombol Force Approve):** Menampilkan dana USDT BEP-20 yang masuk ke wallet tetapi nominalnya tidak pas (misal terpotong fee CEX), lengkap dengan tombol **[ ✅ Pasangkan ke Order ]** yang membuka modal pilih pesanan `PENDING`/`EXPIRED` untuk dilunaskan secara manual (`POST /api/unmatched/resolve`).
   - **Tab 3 (Kelola Toko / API Keys):** Buat toko baru, salin `X-API-Key`, dan hapus toko.
   - **Tab 4 (Konfigurasi Wallet & Node RPC BSC):** Ubah alamat dompet penerima (`0x...`), daftar URL RPC BSC, serta tombol **[ ⚡ Tes Koneksi RPC & Cek Blok Terbaru ]**.
2. **`dashboard/sandbox.html` (Simulator Sandbox — Tema Dark Emerald `#10B981`):**
   - Banner jelas bertuliskan mode pengujian terisolasi tanpa kripto asli.
   - Tombol **[ + Buat Tagihan Test Cepat ]** dan tombol **[ ⚡ Simulasikan Transfer On-Chain Lunas (1-Klik) ]** pada setiap order `PENDING`.
   - Panduan integrasi cepat lengkap dengan cURL dan contoh payload webhook.

---

## 8. Standar Kualitas & Pengujian Wajib Sebelum Selesai

Setelah seluruh kode selesai ditulis, AI Agent CLI **WAJIB** melakukan verifikasi mandiri berikut:
1. Jalankan `go mod tidy` dan kompilasi `CGO_ENABLED=0 go build -o paymentg-bsc main.go` (atau `.exe` di Windows) tanpa error.
2. Jalankan server di background, lalu uji:
   - `POST /api/config/bsc/test` $\rightarrow$ pastikan berhasil terhubung ke Public RPC BSC dan mengembalikan nomor blok terbaru (`latest_block > 40000000`).
   - Buat tagihan baru di `/api/orders` dan `/api/sandbox/orders` dengan nominal `0.8 USDT` $\rightarrow$ pastikan dialokasikan kode unik mikro Tier 1 (`0.801` s.d `0.825` USDT) dan file QR PNG berhasil di-render.
   - Uji `POST /api/sandbox/orders/:id/pay` $\rightarrow$ pastikan status berubah menjadi `PAID`, menghasilkan `tx_hash` simulasi, dan menembakkan webhook ber-signature HMAC.
   - Uji fitur `POST /api/unmatched/resolve` untuk memastikan alur penyelamatan potongan fee CEX berjalan sempurna.
3. Buat file dokumentasi lengkap `RESULT.md` dan `INTEGRATION_GUIDE.md` (termasuk contoh UI Checkout Toko yang menampilkan peringatan khusus pengirim CEX vs Wallet Pribadi).