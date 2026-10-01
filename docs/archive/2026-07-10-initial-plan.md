# ESB — Event Sourcing Boilerplate Maker

## Overview

CLI tool yang menghasilkan boilerplate Go untuk proyek event sourcing berbasis **Event Sourcing Builder** (ESB) platform. Pola diambil langsung dari tiga proyek nyata: `pasaremas`, `rakit-invest`, `ai-kanban`.

---

## Target Pattern yang Di-generate

Setiap proyek yang di-generate mengikuti arsitektur ini:

```
<module>/
├── main.go
├── go.mod
├── Makefile
├── .env.example
├── private.pem          (di-generate, masuk .gitignore)
├── domain/
│   ├── event.go         (Event type alias + NewEvent helper)
│   ├── errors.go        (error sentinel)
│   └── <aggregate>.go   (aggregate struct + events + Apply/Replay)
├── eventstore/
│   └── client.go        (typed HTTP client ke ESB API)
├── repository/
│   └── eventstore_adapter.go
├── service/
│   └── <name>.go        (load → validate → store pattern)
├── projection/
│   ├── db.go            (GORM models + AutoMigrate)
│   ├── worker.go        (base ProjectionWorker)
│   ├── <name>_row.go    (GORM row struct)
│   ├── <name>_worker.go (goroutine loop + applyEvent)
│   └── query.go         (read queries)
├── server/
│   ├── routes.go
│   └── handler/
│       └── <name>.go
└── wire/
    ├── wire.go          (wire.Build)
    └── providers.go     (provider funcs)
```

---

## Commands

### `esb init <module-name>`

Inisialisasi proyek baru dari nol.

```bash
esb init pasaremas
esb init github.com/ariefsam/my-app
```

Yang di-generate:
- `go.mod` dengan module name dan dependensi standar (gorm, gorilla/mux, golang-jwt, godotenv, shortid)
- `main.go` — signal handling + goroutine projection workers + http.Server
- `Makefile` — target: `run`, `wire`, `test`, `build`, `keygen`
- `.env.example` — ESB_URL, TENANT_ID, PROJECT_ID, JWT_ISSUER, DB_DSN, ADDR
- `.gitignore` — private.pem, *.db, .env
- `domain/event.go` — `Event = eventstore.Event`, `EventRepository` interface, `NewEvent()`
- `domain/errors.go` — sentinel errors umum
- `eventstore/client.go` — full typed client (copy dari pasaremas, sudah battle-tested)
- `repository/eventstore_adapter.go` — `EventStoreAdapter` implementing `EventRepository`
- `projection/db.go` — skeleton dengan `ProjectionCursorRow` + `NewProjectionDB()`
- `wire/wire.go` + `wire/providers.go` — skeleton kosong siap diisi

---

### `esb add aggregate <name>`

Tambah aggregate baru ke proyek yang sudah ada.

```bash
esb add aggregate user
esb add aggregate order
esb add aggregate bank_account
```

Yang di-generate:

**`domain/<name>.go`**:
```go
package domain

const UserAggregateName = "user"

func UserAggregateID(id string) string { return id }

type User struct {
    AggregateID string
    Version     int64
}

func NewUser() *User { return &User{} }

func (u *User) Apply(eventName string, data json.RawMessage) error {
    switch eventName {
    default:
        return nil
    }
}

func (u *User) Replay(events []Event) error {
    for _, e := range events {
        if err := u.Apply(e.EventName, e.Data); err != nil {
            return err
        }
    }
    return nil
}

func (u *User) Exists() bool { return u.Version > 0 }
```

**`service/<name>.go`**:
```go
package service

type UserService struct {
    eventRepo domain.EventRepository
}

func NewUserService(repo domain.EventRepository) *UserService {
    return &UserService{eventRepo: repo}
}

func (s *UserService) store(ctx, aggID, eventName string, version, expectedVersion int64, data any) error { ... }
```

**`projection/<name>_row.go`**:
```go
package projection

type UserRow struct {
    AggregateID string `gorm:"primaryKey"`
    Version     int64
    CreatedAt   int64
    UpdatedAt   int64
}

func (UserRow) TableName() string { return "users" }
```

**`projection/<name>_worker.go`**:
```go
package projection

type UserProjectionWorker struct {
    esClient *eventstore.Client
    db       *gorm.DB
}

func (w *UserProjectionWorker) Run(ctx context.Context) { ... }
func (w *UserProjectionWorker) applyEvent(ctx, e) error { ... }
```

Juga:
- Tambah `UserRow` ke `AutoMigrate(...)` di `projection/db.go`
- Tambah `UserProjectionWorker` ke `wire/providers.go`

---

### `esb add event <aggregate> <EventName> [field:type ...]`

Tambah event type ke aggregate yang sudah ada.

```bash
esb add event user UserRegistered email:string display_name:string firebase_uid:string
esb add event order OrderPlaced amount:int64 currency:string
```

Yang di-update:

**`domain/<aggregate>.go`** — inject event struct + Apply case + constructor:
```go
type UserRegistered struct {
    Email        string `json:"email"`
    DisplayName  string `json:"display_name"`
    FirebaseUID  string `json:"firebase_uid"`
    RegisteredAt int64  `json:"registered_at"`
}

// di dalam Apply():
case "UserRegistered":
    var evt UserRegistered
    if err := json.Unmarshal(data, &evt); err != nil {
        return err
    }
    // TODO: update u fields
    u.Version++
    return nil

// constructor:
func NewUserRegisteredEvent(email, displayName, firebaseUID string) UserRegistered {
    return UserRegistered{
        Email: email, DisplayName: displayName,
        FirebaseUID: firebaseUID,
        RegisteredAt: time.Now().UnixMilli(),
    }
}
```

**`projection/<aggregate>_worker.go`** — inject case ke `applyEvent()`:
```go
case "UserRegistered":
    var data domain.UserRegistered
    if err := json.Unmarshal(e.Data, &data); err != nil {
        return err
    }
    tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&UserRow{
        AggregateID: e.AggregateID,
        // TODO: map fields
        CreatedAt: e.TimeMillis,
        UpdatedAt: e.TimeMillis,
        Version:   e.Version,
    })
```

---

### `esb add handler <name> --aggregate <aggregate>`

Tambah HTTP handler skeleton.

```bash
esb add handler user_auth --aggregate user
```

Yang di-generate:
- `server/handler/<name>.go` — struct + method HTTP handler skeleton
- Update `server/routes.go` — tambah route TODO
- Update `wire/providers.go` — tambah provider

---

### `esb add query <name> --aggregate <aggregate>`

Tambah query function ke projection.

```bash
esb add query user_by_email --aggregate user
```

Yang di-update/generate:
- `projection/query.go` — tambah `GetUserByEmail()` skeleton

---

## Implementation Plan

### Phase 1 — CLI Skeleton + `init`

1. Buat `main.go` CLI dengan `cobra` (subcommands)
2. Implementasi `esb init`:
   - Embed template files di Go binary (`embed.FS`)
   - `text/template` untuk substitusi `{{.ModuleName}}`, `{{.PackageName}}`
   - Generate semua file baseline
   - Jalankan `go mod tidy` otomatis
3. Copy `eventstore/client.go` dari pasaremas sebagai template definitif

### Phase 2 — `add aggregate`

1. Parser: detect apakah file sudah ada (buat baru vs skip)
2. Template untuk domain file, service file, projection row, projection worker
3. AST injection ke `projection/db.go` — tambah model ke `AutoMigrate()`
4. Text injection ke `wire/providers.go` — tambah provider stub

### Phase 3 — `add event`

1. Go AST parser untuk:
   - Temukan `Apply()` function → inject `case` baru ke switch
   - Append struct definition
   - Append constructor function
2. Inject ke projection worker `applyEvent()` switch
3. Validasi: aggregate harus sudah ada

### Phase 4 — `add handler` + `add query`

1. Template handler
2. Inject route ke `server/routes.go`
3. Template query

### Phase 5 — Polish

1. Dry-run flag (`--dry-run`) — print apa yang akan di-generate tanpa menulis
2. `esb list` — tampilkan aggregates dan events yang sudah ada di proyek
3. Validasi naming convention (PascalCase untuk EventName, snake_case untuk aggregate name)
4. `go fmt` otomatis setelah generate
5. README template yang di-generate bersamaan dengan `init`

---

## Technical Choices

| Concern | Choice | Alasan |
|---|---|---|
| Language | Go | Same stack dengan target proyek |
| CLI framework | `cobra` | Standard, subcommand support |
| Templates | `text/template` + `embed.FS` | Template di-embed ke binary, tidak perlu install terpisah |
| AST modification | `go/ast` + `go/format` | Type-safe injection, tidak rusak format |
| Fallback AST | regex/text marker comments | Untuk kasus sederhana (inject ke switch) |
| Distribution | Single binary | `go install`, tidak ada dependency |

---

## File Modification Strategy

Ada dua jenis file yang di-handle:

1. **File baru** → generate dari template penuh
2. **File existing yang perlu dimodifikasi** → gunakan marker comment sebagai injection point

Marker comment strategy:
```go
// esb:inject:aggregate-imports
// esb:inject:apply-cases
// esb:inject:automigrate-models
// esb:inject:wire-providers
```

Setiap `esb add` command mencari marker ini dan menginjeksi kode di posisi yang tepat. Jika marker tidak ditemukan (file di-edit manual), tool memberikan warning dan print kode yang perlu ditambahkan secara manual.

---

## Module Structure

```
esb/
├── main.go
├── go.mod
├── cmd/
│   ├── init.go
│   ├── add_aggregate.go
│   ├── add_event.go
│   ├── add_handler.go
│   └── add_query.go
├── generator/
│   ├── project.go       (init project)
│   ├── aggregate.go
│   ├── event.go
│   ├── handler.go
│   └── query.go
├── injector/
│   ├── ast.go           (go/ast based injection)
│   └── marker.go        (marker comment injection)
├── templates/
│   ├── domain_event.go.tmpl
│   ├── domain_aggregate.go.tmpl
│   ├── eventstore_client.go.tmpl
│   ├── repository_adapter.go.tmpl
│   ├── service.go.tmpl
│   ├── projection_row.go.tmpl
│   ├── projection_worker.go.tmpl
│   ├── projection_db.go.tmpl
│   ├── main.go.tmpl
│   ├── go.mod.tmpl
│   ├── makefile.tmpl
│   └── env.example.tmpl
└── naming/
    └── conv.go          (UserName → user_name conversions)
```

---

## Naming Conventions

ESB enforces conventions yang sama dengan proyek referensi:

| Input | Generated |
|---|---|
| `user` aggregate | `UserAggregateName = "user"`, `UserAggregateID()`, `domain/user.go` |
| `bank_account` aggregate | `BankAccountAggregateName = "bank-account"`, `domain/bank_account.go` |
| `UserRegistered` event | struct `UserRegistered`, constructor `NewUserRegisteredEvent()`, case `"UserRegistered"` |
| `order` service | `OrderService`, file `service/order.go` |

---

## Prioritas MVP

Untuk bisa langsung dipakai dalam satu session kerja:

1. `esb init` yang menghasilkan proyek bisa-compile
2. `esb add aggregate` yang menghasilkan domain + projection skeleton
3. `esb add event` yang inject ke Apply() + projection worker

Sisanya (handler, query, polish) bisa menyusul.
