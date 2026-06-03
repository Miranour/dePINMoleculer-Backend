# DePIN Molecular Backend - Master Implementation Plan

> **Hedef:** Kurumsal kalitede, hile korumalı, asenkron ve hataya toleranslı bir backend & dağıtık sistem mimarisi.

## Phase 1: Proje Altyapısı ve Klasör Hiyerarşisi
- [x] Go backend repozitorisinin başlatılması (`go mod init depin-backend`).
- [x] Rust Spot-Checking (Çapraz Doğrulama) projesinin başlatılması (`cargo new validator-engine --bin`).
- [x] Klasör yapısının oluşturulması:
  - Go tarafında: `cmd/server/`, `internal/api/`, `internal/repository/`, `internal/service/`, `internal/models/`, `pkg/database/`, `pkg/queue/`
  - Rust tarafında: `src/`, `proto/` (veya ortak proto dizini)
- [x] Yapılandırma yöneticisinin (Go için `Viper`, Rust için `config`) kurulması ve `.env` desteğinin eklenmesi.
- [x] Teknoloji seçimi netleştirilmiş `docker-compose.yml` oluşturulması:
  - **PostgreSQL 15+** (Port: 5432)
  - **Redis 7+** (Port: 6379)
  - **RabbitMQ 3.12+** (Port: 5672, Management: 15672)
- [x] Go ve Rust projelerinden bu servislere ilk bağlantı (healthcheck/ping) kodlarının yazılması.

## Phase 2: Protobuf (gRPC) Sözleşmeleri & Kod Üretimi
- [x] Ortak `proto/simulation_worker.proto` dosyasının yazılması.
- [x] Mesaj tanımlarının eklenmesi:
  - `HeartbeatRequest`, `HeartbeatResponse` (içinde `repeated RewardNotification recent_rewards`)
  - `WorkerSignal`, `BackendSignal` (Oneof yapısıyla `JobAssignment` ve `AbortSignal` taşıyacak)
  - `WorkerProgress`, `JobResult` (içinde `bytes output_pdbqt_data`, `binding_energy` ve `WorkerErrorCode`)
  - `RewardNotification` (job_id, net_amount, new_confirmed_balance, status)
- [x] Go tarafında `protoc` aracı ile `pb` paketinin üretilmesi.
- [x] Rust tarafında `tonic-build` kullanılarak gRPC istemci ve sunucu kodlarının üretilmesi.

## Phase 3: Veritabanı ve Şema Göçleri (PostgreSQL)
- [x] `golang-migrate` entegrasyonunun tamamlanması.
- [x] Migrations SQL dosyalarının hazırlanması:
  - `user_wallets` (id, user_id, active_balance, blocked_balance, updated_at)
  - `ledger_entries` (id, job_id, amount, type [BLOCK, UNBLOCK, PLATFORM_FEE, WORKER_PAYOUT], created_at)
  - `worker_wallets` (id, worker_id, owner_email, wallet_address, bank_iban, pending_balance, confirmed_balance, total_earned, total_withdrawn, is_frozen, frozen_reason, updated_at)
  - `worker_earnings` (id, worker_id, job_id, gross_amount, platform_fee, net_amount, compute_time_ms, gpu_model, status [PENDING, SPOT_CHECK, CONFIRMED, REJECTED, PAID], spot_check_id, confirmed_at)
  - `withdrawal_requests` (id, worker_id, amount, currency, payout_method, destination, status [PENDING, APPROVED, PROCESSING, COMPLETED, REJECTED], tx_hash, requested_at, processed_at)
- [x] Repository katmanında Go `pgx` veya SQLX adaptörünün yazılması.
- [x] **Önemli:** Veritabanı düzeyinde `SELECT FOR UPDATE` kullanarak çifte harcamayı (double-spending) engelleyecek atomik transaction yardımcı fonksiyonlarının yazılması.

## Phase 4: State Management & Redis Katmanı
- [x] Redis bağlantı havuzunun (go-redis) yapılandırılması.
- [x] `worker:status:{worker_id}` (Hash) yapısının 30s TTL ile kaydedilmesi (Heartbeat ile güncellenecek).
- [x] Redis Redlock dağıtık kilit mekanizmasının kurulması (`lock:job:{job_id}`).
- [x] Hata ve engelleme sayaçları:
  - Hata sayacı: `worker:errors:{worker_id}` (TTL: 1 saat, maks limit: 3).
  - Kara liste kilidi: `worker:blacklist:{worker_id}` (TTL: 10 dakika).
- [x] Watchdog Servisi (Go): Redis Keyspace Notifications (`__keyevent@0__:expired`) dinlenerek 30s içinde heartbeat yollamayan worker'ların aktif işlerinin kurtarılması (PENDING yapılıp kuyruğa iade edilmesi).

## Phase 5: Spot-Checking Engine (Rust)
- [x] Rust Tonic ile gRPC server altyapısının kurulması.
- [x] `ValidateJob` metodunun implementasyonu:
  - İki farklı worker'ın yolladığı `output_pdbqt_data` (koordinat verisi) ve `binding_energy` farklarının hesaplanması.
  - Tolerans eşiği kontrolü ($\Delta \le 0.01$ kcal/mol).
  - İki sonucun atomik X, Y, Z koordinatlarının parser ile karşılaştırılması.
- [x] Sonucun Go orchestrator'a `ValidationResult` (VALID / FRAUD) olarak dönülmesi.

## Phase 6: İş Kuyruğu ve Orkestrasyon (Go)
- [x] RabbitMQ AMQP bağlantısının yapılması ve `pending_simulations` kuyruğunun `x-max-priority` (Öncelikli Kuyruk) parametresiyle tanımlanması.
- [x] gRPC `StreamJobChannel` metodunun implementasyonu (Bi-directional stream):
  - Worker'dan gelen progress/result sinyallerinin işlenmesi.
  - Worker'a anlık `JobAssignment` veya `AbortSignal` iletilmesi.
- [x] **Spot-Check Seçim Mantığı:** Sisteme giren işlerin %5 olasılıkla "Spot-Check" olarak işaretlenmesi, kuyrukta önceliklendirilmesi ve iki farklı coğrafi/bağımsız worker'a dağıtılması.
- [x] Worker aniden koptuğunda veya hata verdiğinde işin yeniden kuyruğa (re-queue) alınması mantığı.

## Phase 7: Ödül (Reward) ve Finansal Mutabakat (Go)
- [x] Ödül Hesaplama Motoru:
  - Formül: `ödül = base_rate * compute_time_seconds * gpu_tier_multiplier * difficulty_factor`
  - %60 platform, %40 worker kırılımının uygulanması.
  - GPU Tier katsayılarının yapılandırma dosyasından (veya DB tablosundan) çekilmesi (RTX 4090: 1.0, RTX 3060: 0.6 vb.).
- [x] Normal iş (Spot-Check dışı) mutabakatı: Tek bir DB transaction'ı içinde worker cüzdanını güncelleme, kullanıcı bloke bakiyesini düşürme, ledger loglama.
- [x] Spot-Check iş mutabakatı:
  - Sonuçlar gelene kadar `worker_earnings` kaydının `SPOT_CHECK` statüsünde bekletilmesi.
  - Rust validator servisinden onay geldiğinde `CONFIRMED` statüsüne geçilip cüzdana yansıtılması.
  - Rust'tan hata/hile raporu gelirse: Hileli worker cüzdanının dondurulması (`is_frozen = TRUE`), cihazın kalıcı kara listeye alınması ve işin 3. validator worker'a tekrar yönlendirilmesi.
- [x] PayoutProcessor: Her 5 dakikada bir çalışan cron job. `APPROVED` taleplerin doğrulanması, blockchain aktarımı (`tx_hash` simulasyonu/entegrasyonu) ve durumun `COMPLETED` olarak güncellenmesi.

## Phase 8: REST API Gateway, Auth & WebSockets (Go)
- [x] API Gateway (Fiber/Gin) ve JWT/API Key auth middleware yazılması.
- [x] **Araştırmacı API endpoints:**
  - `POST /api/v1/jobs` -> Yeni moleküler simülasyon görevi oluşturma (kullanıcı bakiyesinden bloke koyar ve kuyruğa atar).
  - `POST /api/v1/jobs/{id}/abort` -> Simülasyonu iptal etme (Go içindeki `AbortSignal` akışını tetikler).
- [x] **Worker API endpoints:**
  - `POST /api/v1/workers/register` -> Cihaz kaydı, API key üretimi.
  - `POST /api/v1/workers/auth` -> JWT üretimi.
- [x] **Worker Finans API endpoints:**
  - `GET /api/v1/workers/{id}/wallet` -> Bakiye sorgulama.
  - `GET /api/v1/workers/{id}/earnings` -> Kazanç geçmişi.
  - `POST /api/v1/workers/{id}/withdraw` -> Çekim talebi oluşturma.
- [x] **Admin API endpoints:**
  - `GET /api/v1/admin/withdrawals` -> Bekleyen çekim talepleri listesi.
  - `POST /api/v1/admin/withdrawals/{id}/approve` -> Onay.
  - `POST /api/v1/admin/withdrawals/{id}/reject` -> Red.
- [x] WebSocket Sunucusu: Redis Pub/Sub (`room:job_{job_id}`) dinlenerek anlık `WorkerProgress` verilerinin tarayıcıya (araştırmacı ekranına) canlı aktarılması.

## Phase 9: Test ve Doğrulama (Review & E2E)
- [x] Unit Testler: Matematiksel ödül hesaplama doğruluğu, Redlock çifte istek engelleme testi.
- [x] Entegrasyon Testleri: Worker kayıt -> İş atama -> Başarılı tamamlama ve bakiye mutabakatı.
- [x] Entegrasyon Testleri: Spot-Check hata/hile durumu ve ceza akışı testi.
- [x] Entegrasyon Testleri: Watchdog testi (Worker koparılınca işin RabbitMQ kuyruğuna güvenle dönmesi).
