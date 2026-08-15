# esb — Event Sourcing Boilerplate

CLI tool untuk scaffolding proyek Go berbasis [Event Sourcing Builder](https://github.com/ariefsam/event-sourcing-builder). Satu command menghasilkan struktur lengkap yang sudah bisa di-compile: domain aggregate, service, projection worker, repository adapter, dan dependency injection via Google Wire.

---

## Install

```bash
go install github.com/ariefsam/esb@latest
```

Pastikan `$(go env GOPATH)/bin` ada di `PATH`.

---

## Quick Start

```bash
# 1. Buat proyek baru
esb init toko-online

cd toko-online

# 2. Salin .env.example ke .env — default mode adalah "embedded" (lokal),
#    jadi kamu bisa langsung jalan tanpa setup server ESB.
cp .env.example .env

# 3. Tambah aggregate
esb add aggregate order

# 4. Tambah event ke aggregate
esb add event order OrderPlaced amount:int64 currency:string buyer_id:string
esb add event order OrderCancelled reason:string

# 5. Tambah HTTP handler
esb add handler place_order --aggregate order

# 6. Generate wire dan jalankan (mode embedded, tidak butuh server ESB)
make wire
make run
```

Saat server jalan kamu bisa eksplor lewat UI dashboard:

```bash
# Terminal lain
esb ui
# buka http://127.0.0.1:8787 — halaman /storage menampilkan mode aktif
# dan event tersimpan
```

Untuk migrasi ke server ESB remote (production), lihat [Migrasi ke ESB Server](#migrasi-ke-esb-server).

---

## Commands

### `esb init <module-name>`

Inisialisasi proyek baru. `module-name` adalah Go module path — bisa nama pendek atau full path.

```bash
esb init toko-online
esb init github.com/myorg/toko-online
```

Struktur yang dihasilkan:

```
toko-online/
├── main.go
├── go.mod
├── Makefile
├── .env.example
├── .gitignore
├── AGENTS.md
├── domain/
│   ├── event.go
│   └── errors.go
├── eventstore/
│   ├── client.go        # HTTP client (dipakai saat mode esb-server)
│   └── local_store.go   # SQLite-backed EventRepository (dipakai saat mode embedded)
├── repository/
│   ├── eventstore_adapter.go  # adapter untuk HTTP client
│   └── local_adapter.go       # adapter untuk local_store (mode embedded)
├── projection/
│   ├── db.go
│   └── query.go
├── server/
│   └── routes.go
└── wire/
    ├── wire.go
    └── providers.go
```

Setelah `init`, salin `.env.example` ke `.env`. Nilai default membuat aplikasi jalan dalam **mode embedded** (SQLite lokal, tanpa server ESB):

```env
# Mode event store: "embedded" (lokal, SQLite) atau "esb-server" (remote HTTP)
# Default: embedded — tidak butuh server ESB hidup untuk develop lokal.
EVENT_STORE_MODE=embedded
# Opsional: lokasi SQLite khusus event store. Kalau kosong, event store
# memakai DB_DSN dan berbagi satu koneksi GORM dengan projection DB.
# Kalau diisi berbeda, event dan projection sengaja memakai file terpisah.
EVENT_STORE_DSN=

# Hanya dipakai saat EVENT_STORE_MODE=esb-server
ESB_URL=http://localhost:8080
TENANT_ID=my-tenant
PROJECT_ID=toko-online
JWT_ISSUER=toko-online
DB_DSN=toko-online.db
ADDR=:9000
```

Saat `EVENT_STORE_MODE=esb-server` kamu juga butuh ECDSA key pair untuk JWT signing:

```bash
make keygen
# menghasilkan private.pem dan mencetak PUBLIC_KEY untuk di-paste ke .env ESB server
```

---

### `esb add aggregate <name>`

Tambah aggregate baru ke proyek yang sudah ada. Nama menggunakan `snake_case`.

```bash
esb add aggregate user
esb add aggregate bank_account
esb add aggregate product_catalog
```

Yang dihasilkan:

| File | Isi |
|------|-----|
| `domain/<name>.go` | Struct aggregate + `Apply()` + `Replay()` + `Exists()` |
| `service/<name>.go` | `<Name>Service` dengan helper `load()`/`store()` (otomatis snapshot tiap N event) |
| `service/<name>_scenario_test.go` | Contoh test Given-When-Then (lihat bawah) — ganti TODO setelah nambah event/command asli |
| `projection/<name>_row.go` | GORM model untuk read model |
| `projection/<name>_worker.go` | Goroutine projection worker |

Yang diupdate otomatis:

- `projection/db.go` — tambah model ke `AutoMigrate()`
- `wire/providers.go` — tambah provider stub

Contoh output `domain/order.go`:

```go
package domain

const OrderAggregateName = "order"

func OrderAggregateID(id string) string { return id }

type Order struct {
    AggregateID string
    Version     int64
}

func NewOrder() *Order { return &Order{} }

func (o *Order) Apply(eventName string, data json.RawMessage) error {
    switch eventName {
    // esb:inject:apply-cases
    default:
        return nil
    }
}

func (o *Order) Replay(events []Event) error {
    for _, e := range events {
        if err := o.Apply(e.EventName, e.Data); err != nil {
            return err
        }
    }
    return nil
}

func (o *Order) Exists() bool { return o.Version > 0 }
```

`service/order.go` menyediakan dua helper yang dipakai command method buatan sendiri:

- `load(ctx, id)` — muat snapshot terakhir (kalau ada) lalu replay event setelahnya; kalau belum ada snapshot, replay dari awal.
- `store(ctx, agg, eventName, data)` — simpan event dengan optimistic locking, apply ke `agg` supaya state di memori tetap sinkron, dan tiap `OrderSnapshotInterval` versi (default 100) otomatis simpan snapshot baru. Gagal simpan snapshot tidak menggagalkan command — event-nya sudah tersimpan duluan, snapshot murni optimisasi baca.

---

### `esb add event <aggregate> <EventName> [field:type ...]`

Tambah event type ke aggregate yang sudah ada. `EventName` menggunakan `PascalCase`. Field bertipe Go primitive: `string`, `int64`, `float64`, `bool`.

```bash
esb add event order OrderPlaced amount:int64 currency:string buyer_id:string
esb add event order OrderShipped tracking_number:string shipped_at:int64
esb add event order OrderCancelled reason:string cancelled_at:int64
esb add event user UserRegistered email:string display_name:string
```

Yang diinjeksi ke **`domain/<aggregate>.go`**:

```go
// Event struct
type OrderPlaced struct {
    Amount    int64  `json:"amount"`
    Currency  string `json:"currency"`
    BuyerID   string `json:"buyer_id"`
    PlacedAt  int64  `json:"placed_at"`
}

// Constructor
func NewOrderPlacedEvent(amount int64, currency, buyerID string) OrderPlaced {
    return OrderPlaced{
        Amount:   amount,
        Currency: currency,
        BuyerID:  buyerID,
        PlacedAt: time.Now().UnixMilli(),
    }
}

// Case di dalam Apply():
case "OrderPlaced":
    var evt OrderPlaced
    if err := json.Unmarshal(data, &evt); err != nil {
        return err
    }
    // TODO: update o fields
    o.Version++
    return nil
```

Yang diinjeksi ke **`projection/<aggregate>_worker.go`**:

```go
case "OrderPlaced":
    var data domain.OrderPlaced
    if err := json.Unmarshal(e.Data, &data); err != nil {
        return err
    }
    tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&OrderRow{
        AggregateID: e.AggregateID,
        // TODO: map fields dari data ke row
        CreatedAt: e.TimeMillis,
        UpdatedAt: e.TimeMillis,
        Version:   e.Version,
    })
```

---

### `esb add handler <name> --aggregate <aggregate>`

Tambah HTTP handler skeleton.

```bash
esb add handler place_order --aggregate order
esb add handler cancel_order --aggregate order
esb add handler user_auth --aggregate user
```

Yang dihasilkan:

- `server/handler/<name>.go` — struct handler + satu method HTTP skeleton
- Update `server/routes.go` — tambah route dengan `// TODO` comment
- Update `wire/providers.go` — tambah provider stub

---

### `esb add query <name> --aggregate <aggregate>`

Tambah query function ke `projection/query.go`.

```bash
esb add query order_by_buyer --aggregate order
esb add query user_by_email --aggregate user
```

Yang diinjeksi ke `projection/query.go`:

```go
func GetOrderByBuyer(ctx context.Context, db *gorm.DB, buyerID string) ([]OrderRow, error) {
    var rows []OrderRow
    err := db.WithContext(ctx).Where("buyer_id = ?", buyerID).Find(&rows).Error
    return rows, err
}
```

---

### `esb add recipe <kind> ...`

Recipe men-scaffold **satu pola event-sourcing lengkap** dalam satu langkah —
bukan satu komponen — termasuk command, projection, query, handler, dan
scenario test Given-When-Then. Semua ditulis dalam satu transaksi yang
atomik: kalau ada bagian gagal, tidak ada file yang ditulis.

Tersedia juga dari web UI (`esb ui`).

#### `esb add recipe crud <name> [field:type ...]`

Entity CRUD dengan **soft delete** (bukan hapus baris).

```bash
esb add recipe crud product name:string price:int64 sku:string
```

Menghasilkan: aggregate + event `Created`/`Updated`/`Archived`, service dengan
command `Create`/`Update`/`Archive` (invariant di aggregate: sudah-ada /
tidak-ada, Archive idempoten), read model + projection worker, query
`List<Name>s`/`Get<Name>`, handler HTTP write-side, dan scenario test (jalur
sukses + gagal).

#### `esb add recipe ledger <name>`

Akun ledger append-only — contoh kanonik event sourcing (invariant + concurrency).

```bash
esb add recipe ledger account
```

Menghasilkan: event `Opened`/`Deposited`/`Withdrawn`/`Frozen`/`Closed`, command
`Open`/`Deposit`/`Withdraw`/`Freeze`/`Close` dengan **invariant saldo
non-negatif** (uang `int64` minor unit, bukan float), balance read model +
statement journal yang **idempoten**, query saldo/mutasi, handler, dan scenario
test termasuk **concurrency no-double-spend** (lolos `-race`).

#### `esb add recipe statemachine <name> --states ... --transitions ...`

Aggregate lifecycle dengan transisi berpenjaga.

```bash
esb add recipe statemachine order \
  --states placed,paid,shipped,delivered,cancelled \
  --transitions "placed->paid,paid->shipped,shipped->delivered,placed->cancelled,paid->cancelled"
```

State di-derive murni dari event. Menghasilkan: satu event per state +
transition table, command `Transition(ctx, id, to)` yang **menolak transisi
ilegal & state tak dikenal**, read model current-state + query by-state,
handler, dan scenario test (transisi valid & ilegal). `--states` pertama adalah
state awal (satu-satunya yang boleh dimasuki aggregate baru).

#### `esb add recipe saga <name>`

Orchestration saga (process manager): transfer dua-langkah dengan **kompensasi**.

```bash
esb add recipe saga money_transfer
```

Menghasilkan: aggregate saga (Requested→Debited→Credited→Completed, atau
→Failed / →Compensated), command `Transfer(ctx, id, from, to, amount)` yang
menggerakkan Debit lalu Credit lewat sebuah **`<Name>Port` interface**, dan
**me-refund source** kalau credit gagal (tak ada uang hilang). Kegagalan leg
adalah *outcome domain* (event Failed/Compensated), bukan error Go. Sebuah stub
port yang hanya nge-log ikut di-generate & di-wire agar proyek langsung
compile — ganti dengan adapter asli (mis. yang memanggil `AccountService`).
Termasuk read model + query by-state, handler, dan scenario test (happy /
debit-gagal / credit-gagal-kompensasi).

#### `esb add recipe outbox <name>`

**Transactional outbox** untuk integration events dari sebuah aggregate.

```bash
esb add recipe outbox order
```

Menghasilkan dua worker: **ingest worker** yang menulis tiap event `<name>` ke
tabel outbox (idempoten by source event id), dan **publisher worker** yang
mem-poll baris belum-terkirim lalu meneruskannya lewat sebuah `<Name>Publisher`
port (stub log ikut di-generate & di-wire — ganti dengan adapter asli, mis. ke
message bus/webhook), menandai `published` saat sukses (**at-least-once** —
consumer downstream harus idempoten). Termasuk query unpublished + test
(idempotent ingest / publish / retry-on-failure). Kedua worker di-wire ke App
dan dijalankan di `main.go`.

---

### `esb add upcaster <aggregate> <EventName>`

Daftarkan **upcaster** — fungsi yang memigrasikan payload event lama ke bentuk
terbaru **saat dibaca**. Setiap `Replay`/`load` merutekan event stored lewat
rantai upcaster sebelum `Apply`, jadi `Apply` selalu melihat bentuk terbaru.

```bash
esb add upcaster order OrderPlaced
```

Menghasilkan `domain/upcast_<agg>_<event>.go` berisi stub identity + auto-register.
Edit fungsinya saat kamu rename/split/hitung field; jalankan lagi untuk
menambah upcaster berikutnya (rantai v1→v2→v3). Default identity aman kalau kamu
hanya **menambah** field (field yang hilang jadi zero value). Registry
`domain.Upcast` no-op sampai ada upcaster pertama.

---

### `esb add idempotency`

Generate **idempotency guard** yang bisa dipakai ulang (`service/idempotency.go`)
supaya command aman di-retry. Berbasis `IdempotencyKey` di event stream — tanpa
tabel tambahan.

```bash
esb add idempotency
```

Menghasilkan helper `AlreadyProcessed` + `Once`. Bungkus command:

```go
func (s *OrderService) Place(ctx context.Context, id, commandID string) error {
    return service.Once(ctx, s.eventRepo, domain.OrderAggregateName, id, commandID, func() error {
        agg, err := s.load(ctx, id)
        if err != nil { return err }
        // ... validasi invariant ...
        // simpan event dengan IdempotencyKey = commandID
        return s.store(ctx, agg, "OrderPlaced", data)
    })
}
```

Submission ganda (retry, dup jaringan) dengan `commandID` sama → dikenali &
di-skip. Key kosong menonaktifkan guard. Di-generate sekali per proyek.

---

### `esb delete event <aggregate> <EventName>`

Kebalikan `esb add event`: hapus **kode** sebuah event dari aggregate — struct,
constructor, case di `Apply()`, dan case di projection worker — secara AST-based
dan atomik (all-or-nothing).

```bash
esb delete event order OrderPlaced
```

**Tidak** menghapus data event yang sudah tersimpan. Kalau event ini pernah
terjadi, baris di event store tetap ada; yang hilang cuma representasi kodenya,
dan replay ke depan akan **diam-diam mengabaikan** event itu. Karena itu cek
event store dulu sebelum menghapus (UI melakukan pengecekan ini otomatis —
lihat `esb ui`). Sebuah upcaster yang menargetkan event akan **memblokir**
penghapusan; hapus upcaster-nya dulu.

---

### `esb show [aggregate-name]`

Cetak ringkasan satu-layar dari proyek saat ini: aggregate + event, handler wiring, projection worker (single/multi), storage & run-workers, dan wire provider graph. Tidak menulis apa-apa — murni baca file hasil generator.

```bash
esb show
esb show order
```

Tanpa argumen: tampilkan semua section. Dengan nama aggregate: fokus hanya ke bagian yang menyentuh aggregate tersebut (events, handlers, query, projection worker, sub-graph wire), sisa proyek tetap ditampilkan ringkas di header.

Contoh output untuk proyek e-commerce kecil:

```
esb show — domain at a glance
==============================================================================
module:  github.com/myorg/toko
package: toko

Aggregates
------------------------------------------------------------------------------
   order     (3 events: OrderPlaced, OrderCancelled, OrderShipped)
   product   (2 events: ProductListed, StockAdjusted)
   user      (1 events: UserRegistered)

Projections — semua
------------------------------------------------------------------------------
  order        [single]  listens: order
  sales_report [multi]   listens: order, product

Handlers — semua
------------------------------------------------------------------------------
  cancel_order  ->  aggregate: order
  place_order   ->  aggregate: order
  list_products ->  aggregate: product

Storage
------------------------------------------------------------------------------
  AutoMigrate: OrderRow, ProductRow, UserRow, SalesReportRow
  Run workers: OrderProjectionWorker, SalesReportProjectionWorker

Wire Graph
------------------------------------------------------------------------------
  App
  +-- OrderProjectionWorker  *projection.OrderProjectionWorker
  |     projection.NewOrderProjectionWorker(...)
  +-- PlaceOrderHandler  *handler.PlaceOrderHandler
  |     handler.NewPlaceOrderHandler(...)
```

---

### `esb ui`

Jalankan web UI lokal untuk proyek ESB saat ini. UI membaca file hasil
generator dengan `inspector.Scan`, menampilkan dashboard, dan
menjalankan command esb yang termasuk allowlist — tanpa menyentuh
event store, scheduler, atau service eksternal apapun.

```bash
esb ui                          # default: http://127.0.0.1:8787
esb ui --addr 127.0.0.1:9001    # bind ke host/port lain
```

Setelah jalan, buka URL yang dicetak di terminal.

Dari halaman detail aggregate kamu bisa **menambah event** (form dengan
aggregate terisi otomatis) dan **menghapus event**. Sebelum hapus, UI mengecek
apakah event itu sudah pernah tersimpan di event store: di mode embedded ia
menampilkan jumlah baris tersimpan dan **mewajibkan konfirmasi** kalau > 0; di
mode esb-server (atau kalau DB belum bisa dibaca) ia jujur bilang tak bisa
verifikasi dan tetap minta konfirmasi. Penghapusan hanya mengubah kode — data
event store tidak disentuh.

#### Routes

| Method | Route | Deskripsi |
|---|---|---|
| `GET`  | `/healthz` | smoke endpoint, selalu 200 kalau proses hidup |
| `GET`  | `/` | dashboard: module, aggregates, events, projections, handlers, queries, storage |
| `GET`  | `/aggregates/{name}` | detail satu aggregate: events (dengan tombol **Add event** + **Delete**), handlers, query, projection worker |
| `GET`, `POST` | `/aggregates/{name}/events/{event}/delete` | halaman konfirmasi hapus event: cek data tersimpan dulu, lalu jalankan `esb delete event` |
| `GET`  | `/commands` | katalog command + form (dikelompokkan: Scaffold / Recipes / Evolusi / Proyek) |
| `POST` | `/commands/execute` | validasi + jalankan satu command, redirect ke run detail |
| `GET`  | `/commands/runs/{id}` | status, stdout/stderr, exit code |
| `GET`  | `/storage` | mode event store, event/snapshot count per aggregate, isi tabel locks (embedded) |
| `GET`, `POST` | `/storage/migrate` | form + eksekusi migrasi embedded ↔ esb-server |
| `GET`  | `/static/*` | CSS dan helper JS (embedded di binary, offline) |

`POST /commands/execute` mengembalikan `405` untuk method lain,
`400` untuk command/field tidak valid, dan `409` ketika command lain
sedang berjalan.

#### Command yang didukung

UI hanya menjalankan command dari allowlist tertutup — tidak ada
exec arbitrary, tidak ada shell interpolation. Setiap form input
divalidasi sebelum diterjemahkan ke argv.

| Command ID | argv yang dijalankan |
|---|---|
| `add-aggregate` | `esb add aggregate <name>` |
| `add-event` | `esb add event <aggregate> <EventName> [field:type ...]` |
| `add-projection` | `esb add projection <name> --aggregates <a,b,...>` |
| `add-handler` | `esb add handler <name> --aggregate <aggregate>` |
| `add-query` | `esb add query <name> --aggregate <aggregate>` |
| `show` | `esb show [aggregate]` |

Karakter non-`[A-Za-z0-9_-]` (dan `:,.,` pada field event) ditolak
oleh validator — sehingga input seperti `order;touch /tmp/pwned`
tidak pernah menjadi argv dan tidak pernah masuk shell.

#### Keamanan dan limit

- **Hanya localhost.** Default bind ke `127.0.0.1:8787`. `--addr
  0.0.0.0:...` hanya untuk override eksplisit; jangan dipakai di
  jaringan bersama.
- **Tanpa shell.** Runner menggunakan `exec.CommandContext` dengan
  argv slice, bukan `sh -c`.
- **Satu command aktif.** UI menolak eksekusi kedua selama satu
  command masih berjalan, sehingga generator tidak pernah tulis
  paralel ke file yang sama.
- **Bounded run.** Timeout 5 menit per command. Output per stream
  dipotong di 1 MiB dengan marker truncation.
- **Run history in-memory.** Run hilang ketika server berhenti —
  tidak ada persistence ke event store atau database.
- **No CDN.** CSS dan JS di-embed ke binary lewat `//go:embed`. Tidak
  ada request keluar dari browser.
- **CSRF-equivalent.** Origin check pada POST: form submission hanya
  dari same-origin; tidak ada GET dengan efek samping, tidak ada
  arbitrary command/file upload.

Setelah command berhasil, buka overview dan jalankan `esb show` /
refresh browser untuk melihat file yang baru dibuat. UI tidak auto-refresh
— parser adalah snapshot point-in-time.

---

## Cara Kerja (Pattern yang Dihasilkan)

Setiap proyek mengikuti arsitektur ini:

```
HTTP Request
    │
    ▼
Handler (server/handler/)
    │  parse request, panggil service
    ▼
Service (service/)
    │  1. load aggregate dari event store
    │  2. validasi business rules
    │  3. buat event baru
    │  4. store event (dengan optimistic locking)
    ▼
EventRepository (repository/)
    │  adapter ke ESB HTTP API
    ▼
Event Sourcing Builder (HTTP API)
    │  simpan event, assign version
    ▼
Projection Worker (projection/)
    │  long-poll stream events
    │  apply ke read model (SQLite/MySQL via GORM)
    ▼
Query (projection/query.go)
    │  baca dari read model untuk response
    ▼
HTTP Response
```

### Optimistic Locking

Setiap aggregate punya `Version`. Saat menyimpan event, service pass `expectedVersion`:

```go
// Di service, setelah load aggregate:
_, err = s.eventRepo.StoreAtomic(ctx, event, order.Version)
// ESB server reject dengan 409 jika version sudah berubah (race condition)
```

### Snapshot

`service/<aggregate>.go` otomatis menyimpan snapshot tiap `<Aggregate>SnapshotInterval` versi (default 100) lewat `StoreSnapshot`, dan `load()` selalu coba `LatestSnapshot` dulu sebelum replay — jadi aggregate dengan ribuan event tidak perlu replay dari versi 1 tiap kali dipanggil. Bekerja di kedua mode (embedded: tabel `snapshots` lokal; esb-server: endpoint `/snapshots`).

### Distributed Lock

`wire.App.Locker` (tipe `domain.Locker`) menyediakan mutual exclusion lintas replica/instance — berguna untuk leader election, atau memastikan hanya satu instance yang menjalankan job terjadwal tertentu.

```go
lock, err := app.Locker.AcquireLock(ctx, "leader", myInstanceID, 30, 0) // ttlSeconds=30, no wait
switch {
case err == nil:
    // jalan sebagai leader; refresh TTL berkala dari goroutine heartbeat:
    app.Locker.RefreshLock(ctx, "leader", myInstanceID, 30)
case errors.Is(err, domain.ErrLockBusy):
    // instance lain sedang jadi leader
}
```

Bekerja di kedua mode (embedded: tabel `locks` lokal dengan TTL, wait pakai polling sederhana karena single-process; esb-server: endpoint `/locks/*` dengan long-poll cross-process). `esb ui` (`/storage`) menampilkan isi tabel `locks` untuk mode embedded.

### Testing dengan Given-When-Then

Tiap `esb add aggregate` juga men-generate `service/<name>_scenario_test.go` dan (sekali, saat `esb init`) `eventstore/fake_store.go` + package `testkit`. `FakeStore` adalah `EventRepository` in-memory (tanpa SQLite/HTTP) yang punya semantik sama persis dengan `LocalStore`/`Client` (optimistic concurrency, snapshot not-found) — jadi test lewat `testkit` benar-benar menjalankan `load()`/`store()`/command method asli, bukan mock yang disederhanakan.

```go
func TestOrderService_PlaceOrder_RejectsDuplicate(t *testing.T) {
    repo := eventstore.NewFakeStore()
    svc := NewOrderService(repo)

    testkit.Given(t, repo, domain.OrderAggregateName, "order-1",
        testkit.Event("OrderPlaced", map[string]any{"amount": int64(1000)}),
    ).
        When(func(ctx context.Context) error {
            return svc.PlaceOrder(ctx, "order-1", 2000)
        }).
        ThenError(ErrOrderAlreadyPlaced)
}
```

- `Given(t, repo, aggregateName, aggregateID, events...)` — seed event masa lalu (boleh kosong untuk aggregate baru).
- `.When(func(ctx) error { ... })` — panggil command method asli.
- `.Then(events...)` — assert event baru yang tersimpan (dibandingkan by value JSON, bukan string persis).
- `.ThenError(target)` — assert error dari `When` cocok lewat `errors.Is`.

### Projection Cursor

Projection worker melacak posisi terakhir yang diproses. Saat restart, ia lanjut dari posisi terakhir — tidak ada event yang dilewati atau diproses dua kali:

```go
func (w *OrderProjectionWorker) Run(ctx context.Context) {
    for {
        cursor := w.readCursor(ctx)
        events, err := w.esClient.EventsAll(ctx, []string{"order"}, uint(cursor), 100)
        // proses events, update cursor dalam satu transaksi
    }
}
```

### Projection Multi-Aggregate

Satu projection worker bisa mendengarkan lebih dari satu aggregate. Ini berguna ketika satu read model dibangun dari event beberapa aggregate — misalnya laporan keuangan yang menggabungkan `order` dan `payment`, atau dashboard yang perlu data `user` dan `subscription` sekaligus.

```bash
esb add projection balance_summary --aggregates order,payment
```

Yang dihasilkan adalah worker dengan `applyEvent()` yang mem-branch berdasarkan `e.AggregateName`:

```go
// projection/balance_summary_worker.go
type BalanceSummaryProjectionWorker struct {
    esClient *eventstore.Client
    db       *gorm.DB
}

func (w *BalanceSummaryProjectionWorker) Run(ctx context.Context) {
    for {
        cursor := w.readCursor(ctx)
        // EventsAll menerima slice aggregate names — long-poll bangun saat salah satu ada event baru
        events, err := w.esClient.EventsAll(ctx, []string{"order", "payment"}, uint(cursor), 100)
        // ...
    }
}

func (w *BalanceSummaryProjectionWorker) applyEvent(ctx context.Context, e eventstore.Event) error {
    return w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        switch e.AggregateName {
        case "order":
            switch e.EventName {
            case "OrderPaid":
                // update balance_summary dari sisi order
            }
        case "payment":
            switch e.EventName {
            case "PaymentConfirmed":
                // update balance_summary dari sisi payment
            }
        }
        // update cursor dalam transaksi yang sama
        return w.writeCursor(tx, e.ID)
    })
}
```

Cursor per worker (bukan per aggregate) — cukup satu baris `ProjectionCursorRow` dengan nama worker sebagai key, karena semua aggregate diproses dalam satu stream berurutan.

Worker single-aggregate (`esb add aggregate`) dan multi-aggregate (`esb add projection --aggregates`) menggunakan pola yang sama; perbedaannya hanya pada panjang slice yang di-pass ke `EventsAll` dan ada tidaknya branch `e.AggregateName` di `applyEvent()`.

---

## Autentikasi ke ESB Server

ESB menggunakan JWT ES256 (ECDSA P-256). Setiap request ditandatangani dengan private key:

```bash
# Generate key pair
make keygen
# Output:
#   private.pem  — simpan di server, jangan commit ke git
#   PUBLIC_KEY=LS0tLS1CRUdJTiBQVUJMSUMgS0VZLS0tLS0...

# Paste PUBLIC_KEY ke .env ESB server:
# PUBLIC_KEYS=LS0tLS1CRUdJTiBQVUJMSUMgS0VZLS0tLS0...
```

Client library menandatangani dan memperbarui token secara otomatis — tidak perlu manajemen token manual.

---

## Injection Points

`esb` menggunakan marker comment untuk mengetahui di mana harus menginjeksi kode ke file yang sudah ada. Jangan hapus marker ini:

| Marker | Lokasi | Digunakan oleh |
|--------|--------|----------------|
| `// esb:inject:apply-cases` | `domain/<agg>.go` dalam `Apply()` | `add event` |
| `// esb:inject:automigrate-models` | `projection/db.go` | `add aggregate`, `add projection` |
| `// esb:inject:wire-providers` | `wire/providers.go` | `add aggregate`, `add projection`, `add handler` |
| `// esb:inject:routes` | `server/routes.go` | `add handler` |

Jika marker tidak ditemukan (file diedit manual), `esb` akan print kode yang perlu ditambahkan secara manual.

---

## Dry Run

Gunakan flag `--dry-run` untuk melihat apa yang akan di-generate tanpa menulis ke disk:

```bash
esb add aggregate invoice --dry-run
esb add event order OrderShipped tracking_number:string --dry-run
```

---

## Contoh Alur Lengkap

```bash
# Init proyek e-commerce
esb init github.com/myorg/toko

cd toko
cp .env.example .env
make keygen   # simpan output PUBLIC_KEY ke ESB server

# Domain: order
esb add aggregate order
esb add event order OrderCreated buyer_id:string total_amount:int64
esb add event order OrderPaid payment_id:string paid_at:int64
esb add event order OrderShipped tracking_number:string
esb add event order OrderDelivered delivered_at:int64
esb add event order OrderCancelled reason:string

# Domain: product
esb add aggregate product
esb add event product ProductListed name:string price:int64 stock:int64
esb add event product ProductPriceUpdated new_price:int64
esb add event product StockAdjusted delta:int64

# Handler
esb add handler create_order --aggregate order
esb add handler pay_order --aggregate order
esb add handler list_orders --aggregate order

# Query
esb add query orders_by_buyer --aggregate order
esb add query orders_by_status --aggregate order

# Projection multi-aggregate: laporan penjualan gabungan order + product
esb add projection sales_report --aggregates order,product

# Generate wire, build, run
make wire
make run
```

---

## Lisensi

MIT
