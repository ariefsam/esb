# Ide: Code Generator untuk Pola Umum Event Sourcing

> Draf ide, 2026-08-14. Tujuan: menaikkan esb dari **primitive generator**
> (`add aggregate/event/handler/query/projection`) menjadi generator yang juga
> bisa men-scaffold **pola/blueprint utuh** yang lazim di event sourcing —
> CRUD, ledger bank, state machine, saga, inventory, dll — lengkap dengan
> invariant, command, projection, dan test Given-When-Then.

---

## 1. Kenapa "blueprint", bukan sekadar `add event` lagi

Perintah `add` sekarang bersifat **primitif**: mereka menaruh satu event, satu
handler, atau satu query. Bagus untuk fleksibilitas, tapi pengguna baru harus
tahu sendiri **pola**-nya:

- Command harus memvalidasi invariant **di aggregate** (`load → cek → store`),
  bukan di handler.
- "Delete" di ES itu **soft** (event `Archived`/`Closed`), bukan hapus baris.
- Uang harus pakai **integer minor unit** + invariant saldo non-negatif, dan
  transfer antar-akun butuh **saga/process manager**, bukan satu command.
- Command yang di-retry harus **idempoten** (dedup by command id).

Semua ini adalah **pengetahuan yang bisa di-generate**. Blueprint = satu perintah
yang menjalankan beberapa `add` + mengisi domain logic + scenario test, sehingga
pengguna dapat vertical slice yang benar sejak awal.

### Bentuk CLI yang diusulkan

```bash
esb add recipe crud product name:string price:int64 sku:string
esb add recipe ledger account currency:string
esb add recipe statemachine order --states placed,paid,shipped,delivered,cancelled
esb add recipe saga money_transfer --from account --to account
esb add recipe inventory stock_item --track reserved,on_hand
```

`recipe` = generator tingkat tinggi yang **mengomposisi** perintah `add` yang
sudah ada + template domain-logic tambahan. Karena `injector.Tx` (transaksional)
sudah ada, seluruh blueprint bisa ditulis sebagai **satu commit file** yang
all-or-nothing — kalau satu langkah gagal, tidak ada yang ditulis.

Prinsip: blueprint **tidak** memperkenalkan runtime baru; ia hanya menyusun
pola di atas primitif `domain/service/projection/handler/wire` yang ada.

---

## 2. Katalog blueprint (berprioritas)

### 🟢 P1 — CRUD (create/update/soft-delete) — `esb add recipe crud`

Pola paling diminta & paling sering salah di ES ("mana DELETE-nya?").

**Generate:**
- Events: `<Name>Created(fields…)`, `<Name>Updated(changedFields…)`,
  `<Name>Archived()` (soft delete).
- Command di service:
  - `Create(ctx, id, fields…)` → guard `if agg.Exists() { return ErrAlreadyExists }`
    lalu `store("<Name>Created", …)`.
  - `Update(ctx, id, patch…)` → guard `if !agg.Exists() || agg.Archived { … }`.
  - `Archive(ctx, id)` → idempoten.
- Projection read-model `<Name>Row` + kolom `archived` (flag; List
  menyembunyikan yang ter-archive, bukan menghapus baris).
- Query: `List<Name>s` (filter `archived_at IS NULL`), `Get<Name>ByID`.
- Handler HTTP untuk tiap command.
- Scenario test GWT: create→update→archive, plus "create dua kali → error",
  "update setelah archive → error".

**Pitfall yang di-encode:** soft-delete, invariant di aggregate, projection yang
menyembunyikan baris ter-archive alih-alih menghapusnya (agar history & audit
tetap utuh).

---

### 🟢 P1 — Ledger / akun bank (double-entry) — `esb add recipe ledger`

Contoh kanonik ES; sekaligus mendemokan **invariant + optimistic concurrency**.

**Generate (aggregate `Account`):**
- Events: `AccountOpened(currency)`, `Deposited(amount, ref)`,
  `Withdrawn(amount, ref)`, `AccountFrozen()`, `AccountClosed()`.
- Uang sebagai `int64` **minor unit** (sen), bukan float — dengan komentar tegas.
- Command:
  - `Open` (guard belum ada), `Deposit` (amount>0),
  - `Withdraw` → invariant `if agg.Balance < amount { return ErrInsufficientFunds }`
    (dicek **di dalam** load→store agar aman terhadap balapan lewat
    `StoreAtomic(expectedVersion)`).
  - `Freeze/Close` menggerbang command lain.
- Projection `AccountBalanceRow` (running balance) + `LedgerEntryRow` (append-only
  jurnal per transaksi, untuk audit).
- Query: saldo, mutasi (statement) per rentang waktu.
- Scenario test: overdraw ditolak, deposit+withdraw seimbang, concurrency
  (dua withdraw paralel, satu kalah `ErrConflict`).

**Ekstensi:** `esb add recipe ledger --double-entry` yang men-generate posting
debit+kredit yang selalu balance (invariant "sum = 0").

---

### 🟢 P1 — State machine / workflow — `esb add recipe statemachine`

Untuk aggregate yang hidup di lifecycle (order, pengajuan, tiket).

```bash
esb add recipe statemachine order \
  --states placed,paid,shipped,delivered,cancelled \
  --transitions "placed->paid,paid->shipped,shipped->delivered,placed->cancelled,paid->cancelled"
```

**Generate:**
- Event per transisi: `OrderPlaced`, `OrderPaid`, … + field `State` di aggregate.
- Tabel transisi yang di-generate + guard: command menolak transisi ilegal
  (`ErrInvalidTransition`) — dicek di aggregate.
- Projection `OrderRow.status` + query `by status`.
- Scenario test: happy path + tiap transisi terlarang ditolak.

**Pitfall yang di-encode:** guard transisi di aggregate, bukan if-else tersebar
di handler; state disimpan sebagai turunan event (bukan kolom yang di-UPDATE
langsung).

---

### 🟡 P2 — Saga / Process Manager — `esb add recipe saga`

Koordinasi lintas-aggregate (transfer uang = debit akun A + kredit akun B,
dengan **kompensasi** kalau salah satu gagal).

**Generate:**
- Sebuah worker yang **listen event** (mirip projection) lalu **mengirim command**
  ke service lain — reaksi, bukan query.
- State saga sendiri (aggregate `MoneyTransfer`: `Requested → Debited → Credited
  → Completed` / `→ Compensated`), sehingga proses **resumable** setelah crash
  (memakai cursor projection yang sudah ada).
- Timeout/step yang gagal → event kompensasi (`Refunded`).
- Scenario test: happy path + "kredit gagal → debit dikompensasi".

**Catatan desain:** ini fitur paling "berat". Bisa dimulai dari template saga
minimal (dua langkah + kompensasi) sebelum generalisasi. Reuse infrastruktur
projection worker (long-poll + cursor transaksional) yang sudah matang.

---

### 🟡 P2 — Inventory / stok dengan reservasi — `esb add recipe inventory`

**Generate:** `StockReceived`, `Reserved`, `ReservationReleased`, `Shipped`;
invariant `on_hand - reserved >= 0`; idempotency per `reservation_id`. Projection
`available = on_hand - reserved`. Mendemokan idempotency + invariant kuantitas.

---

### 🟡 P2 — Wallet / counter / running total — `esb add recipe tally`

Pola read-model agregasi murni: `Credited/Debited` → projection saldo berjalan,
plus query "as of" (lihat §3 temporal). Ringan, bagus sebagai contoh projection.

---

## 3. Fitur generator lintas-pola (bukan blueprint, tapi "aspek")

Ide-ide ini bisa jadi **flag** pada `add`/`recipe`, atau perintah sendiri:

| Fitur | CLI | Yang di-generate |
|---|---|---|
| **Idempotent command** | `--idempotent` | Field `command_id` di event + guard "sudah diproses?" (tabel/projection dedup) sehingga retry aman. |
| **Event versioning / upcaster** | `esb add upcaster order OrderPlaced v1 v2` | Fungsi `upcast<Event>V1toV2(raw) []byte` + hook di `Replay`, agar event lama tetap bisa dibaca. |
| **Temporal / as-of query** | `esb add query balance_as_of --temporal` | Query yang me-`Retrieve` event ≤ `T` lalu `Replay` (rekonstruksi state historis). |
| **Outbox / integration event** | `esb add recipe outbox` | Projection yang menulis ke tabel outbox + worker publisher (stub) untuk integrasi ke sistem lain. |
| **Snapshot tuning** | `--snapshot-interval N` | Set `<Agg>SnapshotInterval` saat generate (sudah ada konstannya). |
| **Policy / reaction** | `esb add policy when OrderPaid do ...` | Worker kecil "when event X → command Y" (saga satu-langkah). |

`upcaster` dan `idempotent` menurut saya **nilai tinggi / effort rendah** dan
langsung menyentuh masalah nyata ES (migrasi skema event, retry).

---

## 4. Prinsip desain agar blueprint tetap sehat

1. **Komposisi, bukan jalur khusus.** Blueprint memanggil `AddAggregate`,
   `AddEvent`, dst. yang sudah ada, lalu menambah template domain-logic. Jangan
   duplikasi wiring; reuse `injector.Tx` supaya seluruh recipe atomik.
2. **Invariant di aggregate.** Command men-`load → cek invariant → store`. Handler
   hanya parsing + panggil service. Template harus mencontohkannya, bukan menaruh
   validasi di handler.
3. **Tidak ada hard delete.** Selalu event `Archived/Closed/Cancelled`; projection
   yang menyembunyikan, bukan `DELETE`.
4. **Uang = integer minor unit.** Tidak pernah float. Sertakan komentar.
5. **Selalu generate scenario test GWT** (testkit + `FakeStore` yang sudah ada),
   termasuk **jalur gagal** (invariant ditolak, konflik versi). Ini yang membuat
   blueprint "benar sejak awal".
6. **Idempotency & concurrency eksplisit.** Manfaatkan `StoreAtomic(expectedVersion)`
   yang sudah ada untuk optimistic locking; tunjukkan test `ErrConflict`.
7. **Interaktif + non-interaktif.** `esb add recipe` harus jalan di CI (hormati
   flag, jangan prompt saat non-TTY) — konsisten dengan perbaikan `init`.

---

## 5. Catatan implementasi (nyambung ke kode sekarang)

- **Tempat kode:** `generator/recipe_*.go` + `generator/templates/recipe/…`.
  Sebuah `Recipe` = daftar langkah (`AddAggregate`, `AddEvent`, render template
  domain-logic, `AddQuery`, `AddProjection`, `AddHandler`) yang semuanya
  di-stage lewat **satu `injector.Tx`** dan di-`Commit()` sekali.
- **Command methods:** saat ini `service.go.tmpl` hanya punya `load/store`;
  method command (mis. `PlaceOrder`) masih TODO. Blueprint mengisi kekosongan
  ini — inilah nilai terbesarnya. Perlu marker baru, mis.
  `// esb:inject:service-commands`, di `service.go.tmpl`.
- **Validasi input:** reuse `validateSnakeName`/`validatePascalName` (#12) untuk
  nama recipe, state, field.
- **Inspector:** karena inspector kini berbasis `go/ast` (#10), pola yang
  di-generate otomatis terbaca `esb show` tanpa regex baru — selama ia memakai
  simbol yang sama (event struct + doc `// X event.`, `Apply` switch, `<X>Row`,
  `func Name(ctx, db)`).
- **Golden test:** tiap blueprint dapat 1 golden test "generate → `go build` →
  `esb show` menemukan komponennya" (pola sama seperti `TestAddFlow` &
  `TestGolden_InspectorParsesGeneratorOutput`).
- **Katalog & dokumentasi:** daftarkan recipe di `esb add recipe --list` dan
  regen `AGENTS.md` agar asisten AI tahu recipe yang tersedia.

---

## 6. Roadmap bertahap

**Fase 1 — fondasi + 1 blueprint. ✅ SELESAI**
1. ✅ Tambah marker `// esb:inject:service-commands` di `service.go.tmpl`
   (fondasi untuk `esb add command` di masa depan).
2. ✅ Implement **CRUD** blueprint: `esb add recipe crud <name> field:type…`
   — domain + Created/Updated/Archived (soft delete), service dengan
   Create/Update/Archive (invariant di aggregate), projection row+worker,
   query List/Get, handler write-side, scenario test GWT (happy + gagal).
   Semua atomik lewat satu `injector.Tx`.
3. ✅ Golden test CRUD (`generator/recipe_crud_test.go`): generate → `go build`
   → `go test ./service/...` (scenario hijau), termasuk dua entity di satu proyek.
   Inspector `esb show` juga diperluas (scan query lintas-file di `projection/`,
   filter file non-handler seperti `response.go`).

**Fase 2 — blueprint domain klasik.**
4. ✅ **Ledger/bank** (`esb add recipe ledger <name>`): Open/Deposit/Withdraw/
   Freeze/Close, invariant saldo non-negatif (uang int64 minor unit), balance
   read model + statement journal idempoten, query balance/statement, handler,
   dan scenario test termasuk **concurrency no-double-spend** (lolos `-race`).
5. ✅ **State machine** (`esb add recipe statemachine <name> --states … --transitions …`):
   satu event per state + transition table, command `Transition(ctx, id, to)`
   berpenjaga (tolak transisi ilegal & unknown state), read model current-state
   + query by-state, handler, scenario test (transisi valid & ilegal).

**Fase 3 — lintas-aggregate & evolusi.**
6. **Idempotent** flag + **upcaster** (nilai tinggi, effort rendah).
7. ✅ **Saga** money-transfer (`esb add recipe saga <name>`): orchestration saga
   dua-langkah (Debit→Credit) dengan **kompensasi** (refund source saat credit
   gagal). Kegagalan leg = outcome domain (event Failed/Compensated), bukan
   error Go. Port interface + stub log (di-wire agar compile), read model +
   query by-state, handler, scenario test (happy / debit-gagal / credit-gagal-
   kompensasi). Diverifikasi runtime.
8. **Outbox** publisher.

**Fase 4 — ergonomi.**
9. `esb add recipe --list` + regen `AGENTS.md`.
10. Interactive wizard opsional (`esb new`) untuk memilih blueprint + field.

---

## 7. Risiko / hal yang harus dijaga

- **Over-scaffolding:** blueprint yang meng-generate terlalu banyak file bisa
  bikin pengguna bingung. Mulai minimal, tambah lewat flag.
- **Opinionated ≠ kaku:** sediakan output yang mudah diedit (TODO yang jelas),
  jangan magic. Blueprint = titik awal, bukan framework.
- **Saga itu sulit** (kompensasi, timeout, idempotency). Jangan generate saga
  yang terlihat lengkap tapi salah; mulai dari dua-langkah yang benar + test.
- **Konsistensi inspector/testkit:** setiap pola baru wajib punya golden test,
  agar drift template ketahuan (pelajaran dari #10/#11).
