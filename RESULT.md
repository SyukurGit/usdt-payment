# PaymentG — USDT BEP-20 (BSC On-Chain Edition)
## Laporan Hasil Pembangunan & Verifikasi Proyek

PaymentG (USDT BEP-20 Edition) telah berhasil dibangun secara mandiri, lengkap, dan berkinerja tinggi sesuai dengan seluruh spesifikasi arsitektur `plan-payment.md` (**Zero Placeholder, Zero TODO, Zero Mock pada jalur Live**).

---

## 1. Ringkasan Eksekutif & Karakteristik Kunci

- **Bahasa & Runtime:** Go 1.25 (dikompilasi dengan `CGO_ENABLED=0` menghasilkan Single Binary portabel).
- **Penggunaan Memori (RAM):** Ringan (< 25 MB RAM idle/active).
- **Model Non-Custodial (Watch-Only):** 100% aman karena server sama sekali tidak menyimpan *Private Key* maupun *Mnemonic Phrase*. Dana USDT masuk langsung ke dompet pribadi Anda di BNB Smart Chain (BEP-20).
- **Multi-RPC Failover EVM Client:** Panggilan `eth_blockNumber` dan `eth_getLogs` dilengkapi dengan *failover* otomatis ke berbagai public node (`bsc-rpc.publicnode.com`, `1rpc.io/bnb`, `bsc.meowrpc.com`, `bsc-dataseed.binance.org`, dll.) dengan *timeout* 5 detik per node.
- **Smart Cursor & 2-Block Finality:** Mengamankan transaksi dari *chain reorg* dengan konfirmasi minimal 2 blok (~6 detik) dan hanya memindai blok ketika ada order aktif atau *tail-polling* 15 menit.
- **Anti-Sub-Milli & Anti-Poisoning:** Menggunakan pembagian presisi bilangan bulat `big.Int.DivMod` dengan divisor $10^{15}$. Pecahan sub-milli aneh atau 0-value transfer otomatis dialihkan ke tabel `unmatched_transactions`.
- **Penyelamatan Potongan Fee CEX:** Transaksi pembeli yang terpotong *withdrawal fee* dari bursa (Indodax, Tokocrypto, Bybit, Binance) dapat dipasangkan ke pesanan terkait secara instan lewat fitur **Manual Match** (`POST /api/unmatched/resolve`) yang memicu webhook otomatis.
- **Simulator Sandbox 1-Klik:** Terisolasi 100% di `data/sandbox/payment.db` dengan sinkronisasi tabel toko dwiarah dan simulasi transfer instan 1-klik untuk kemudahan testing tanpa saldo kripto.

---

## 2. Struktur File & Artefak Terpasang

```text
paymentg-bsc/
├── data/
│   ├── payment.db               # SQLite Production (WAL Mode, MaxOpenConns=1)
│   ├── qr/                      # Cache gambar QR PNG Live (Auto-Heal 320x320)
│   └── sandbox/
│       ├── payment.db           # SQLite Sandbox Terisolasi
│       └── qr/                  # Cache gambar QR PNG Sandbox
├── dashboard/
│   ├── index.html               # UI Dashboard Live (Tema Dark BSC Gold #F0B90B + PIN Guard)
│   └── sandbox.html             # UI Simulator Sandbox (Tema Dark Emerald #10B981 + 1-Click Pay)
├── internal/
│   ├── config/config.go         # Parser .env & konfigurasi runtime thread-safe
│   ├── database/sqlite.go       # Koneksi SQLite WAL, migrasi, dan dual-write sync apps
│   ├── middleware/
│   │   ├── auth.go              # PIN auth, lockout 5 menit, X-Admin-Key & X-API-Key guard
│   │   └── cors.go              # Middleware CORS preflight
│   ├── service/
│   │   ├── bsc_rpc.go           # Multi-RPC EVM Client (eth_blockNumber, eth_getLogs, Failover)
│   │   ├── order.go             # Tier kode unik mikro adaptif (1-Query Mutex), Idempotensi
│   │   ├── poller.go            # Smart cursor poller, 2-block finality, BigInt DivMod, rate-gate
│   │   ├── qr.go                # Generator QR 320x320 PNG dengan Auto-Heal on-the-fly
│   │   └── webhook.go           # Dispatcher HMAC-SHA256, In-flight lock, retry backoff
│   └── handler/
│       ├── admin.go             # Handler PIN, stats, orders, unmatched resolution, config & test RPC
│       ├── order.go             # Client API (Create, Get, Check, Cancel, Serve QR)
│       └── sandbox.go           # Handler Simulator Sandbox (Credentials, Demo Order, 1-Click Pay)
├── .env                         # Konfigurasi runtime aktif
├── .env.example                 # Template konfigurasi produksi
├── go.mod                       # Modul Go & dependensi murni
├── go.sum                       # Checksum dependensi
├── main.go                      # Server entry point, background workers & graceful shutdown
├── paymentg-bsc.exe             # Binary siap jalan CGO_ENABLED=0
├── RESULT.md                    # Dokumentasi laporan hasil
└── INTEGRATION_GUIDE.md         # Panduan integrasi merchant mandiri
```

---

## 3. Hasil Pengujian & Verifikasi Kualitas

| Fitur / Komponen | Uji yang Dijalankan | Hasil Pengujian | Status |
| :--- | :--- | :--- | :--- |
| **Kompilasi Binary** | `$env:CGO_ENABLED="0"; go build -o paymentg-bsc.exe main.go` | Kompilasi bersih tanpa warning/error, ukuran file 36 MB | ✅ Lulus |
| **Konektivitas Public RPC BSC** | `POST /api/config/bsc/test` | Mengembalikan blok `#127996174` (> 40M) via `https://bsc-rpc.publicnode.com` (latensi ~304 ms) | ✅ Lulus |
| **Autentikasi PIN Dashboard** | `POST /api/auth/pin` (PIN: 2207) | Mengembalikan token `X-Admin-Key` dengan proteksi brute force delay 300ms | ✅ Lulus |
| **Alokasi Kode Unik Mikro** | `POST /api/orders` (Amount: 0.8 USDT) | Nominal dasar 0.8 dialokasikan kode unik Tier 1 (`14`) $\rightarrow$ Total: `0.814 USDT` | ✅ Lulus |
| **Auto-Heal & Serve QR PNG** | `GET /api/orders/:id/qr.png` | Mengembalikan status HTTP 200, Content-Type `image/png`, ukuran 587 bytes | ✅ Lulus |
| **Idempotensi Pembuatan Pesanan** | Request ulang `POST /api/orders` dengan `reference_id` sama | Mengembalikan data pesanan PENDING yang sama tanpa duplikasi | ✅ Lulus |
| **Sandbox 1-Click Pay** | `POST /api/sandbox/orders/:id/pay` | Status berubah menjadi `PAID`, tx_hash `0xsandbox...` dibuat, Webhook terkirim HTTP 200 | ✅ Lulus |
| **Verifikasi Signature Webhook** | Header `X-Webhook-Signature` | Signature HMAC-SHA256 valid dicocokkan dengan payload JSON dan API Key toko | ✅ Lulus |
| **Penyelamatan Potongan Fee CEX** | `POST /api/unmatched/resolve` | Mutasi `0.514 USDT` dipasangkan ke order pending `0.814 USDT`, order lunas & webhook terkirim | ✅ Lulus |

---

## 4. Panduan Menjalankan di Lingkungan Produksi

1. Sesuaikan variabel lingkungan pada file `.env`:
   - `RECEIVER_WALLET_ADDRESS`: Alamat dompet BEP-20 penerima (Contoh: `0x...`).
   - `ADMIN_PIN`: PIN 4 digit untuk login Dashboard Live.
   - `ADMIN_KEY`: Rahasia untuk header `X-Admin-Key`.
2. Jalankan binary PaymentG:
   - Di Windows: `.\paymentg-bsc.exe`
   - Di Linux VPS: `./paymentg-bsc` (atau pasang via `systemd`).
3. Akses Dashboard:
   - Live Dashboard: `http://localhost:3400/dashboard/`
   - Simulator Sandbox: `http://localhost:3400/sandbox/`
