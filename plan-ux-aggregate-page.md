# Plan — Tambah & Hapus Event dari halaman `/aggregates/{name}`

> ✅ **SUDAH DIIMPLEMENTASIKAN** (Fase 0–4) dengan default decisions dari §4.
> Inspector event-counts, injector AST-removal, `esb delete event`, dan UI
> add/delete dari halaman aggregate semuanya sudah ada + ter-test. Dokumen ini
> disimpan sebagai catatan desain.
>
> Draft plan asli. Ditulis berdasarkan pembacaan kode
> `ui/`, `generator/`, `injector/`, `inspector/`, `eventstore/`, `cmd/` per
> 2026-08-15. Semua referensi file/baris mengacu ke isi repo saat draft ini
> ditulis.

---

## 1. Tujuan

Di halaman detail aggregate (`GET /aggregates/{name}`, contoh
`http://localhost:8787/aggregates/academic-year`), user bisa:

1. **Tambah event baru** langsung dari halaman itu (tanpa pindah ke `/commands`
   dan ketik ulang nama aggregate).
2. **Hapus event** yang sudah terdaftar di aggregate tersebut — **tapi** hanya
   setelah sistem mengecek apakah event itu **sudah pernah tersimpan** sebagai
   data nyata, baik di:
   - **embedded store** (SQLite lokal, `EVENT_STORE_MODE=embedded`), atau
   - **Event Sourcing Builder server** (remote, `EVENT_STORE_MODE=esb-server`).

   Kalau datanya sudah ada, user harus melihat peringatan eksplisit sebelum
   bisa lanjut menghapus — supaya tidak menghapus definisi event yang
   event-nya sudah pernah terjadi di histori (yang akan bikin replay
   ke depannya diam-diam kehilangan state untuk event itu).

## 2. Kondisi saat ini (baseline)

Ringkasan yang relevan supaya jelas apa yang sudah ada vs yang perlu dibangun:

| Area | Status sekarang |
|---|---|
| `GET /aggregates/{name}` | Read-only. Menampilkan events, handlers, queries, projection workers (`ui/handlers.go:handleAggregate`, `ui/templates/aggregate_detail.html`). Tidak ada form apa pun. |
| Tambah event | Sudah ada, tapi hanya lewat halaman generik `/commands` (`ui/commands.go` catalog id `add-event` → `esb add event <agg> <Event> [field:type...]` → `generator.AddEvent`). User harus ketik manual nama aggregate. |
| Hapus event | **Tidak ada sama sekali** — bukan di CLI (`cmd/`), bukan di generator, bukan di UI. `esb` hanya generator satu-arah: satu-satunya kapabilitas mengubah file yang sudah ada adalah *append*/*inject-after-marker* (`injector/marker.go`, `injector/tx.go`) — tidak ada primitive untuk *menghapus* kode. |
| Cek data event yang sudah ada — embedded | `inspector.ScanStorage` (`inspector/storage.go`) sudah menghitung event **per aggregate** dari tabel `events` (`StorageInfo.Counts map[string]int`, key = `aggregate_name`). **Belum** ada breakdown per `event_name` — jadi belum bisa menjawab "apakah aggregate X pernah punya event `OrderPlaced`". |
| Cek data event yang sudah ada — esb-server (remote) | Client (`eventstore/client.go`) cuma punya `Events(aggregateID, aggregateName, afterVersion)` (butuh 1 aggregate ID spesifik) dan `EventsAll(aggregateNames, afterID, limit)` (long-poll, tidak difilter per event_name). **Tidak ada endpoint count-by-event-name** di sisi client maupun (setahu kami) di server API — itu API server ESB terpisah, di luar repo ini. |
| Argumen aggregate di form command | Field `aggregate` di form `/commands` divalidasi cuma sebagai string alfanumerik (`validateFieldName`) — **tidak divalidasi harus snake_case**, padahal `generator.AddEvent` langsung memakai nilainya sebagai nama file: `domain/<aggregate>.go`. Nama aggregate yang tampil di URL (`academic-year`) adalah **kebab-case** (`Aggregate.Name`, dari konstanta `XxxAggregateName = "academic-year"`), sedangkan nama file aslinya **snake_case** (`Aggregate.FileName`, contoh `academic_year`). Kalau form baru di halaman aggregate mengisi field `aggregate` dengan `Name` (kebab) alih-alih `FileName` (snake), `esb add event academic-year ...` akan gagal karena `domain/academic-year.go` tidak ada. **Ini gotcha penting** yang harus dihandle di implementasi (pakai `selected.FileName`, bukan `resolved`/`Name`, saat mengisi field `aggregate` di form baru). |

## 3. Desain fitur

### 3.1 Tambah event dari halaman aggregate

Paling murah dan paling rendah risiko — infrastrukturnya (`add-event` di
catalog, `generator.AddEvent`, `injector.Tx`) sudah lengkap dan sudah teruji.
Yang perlu dibangun cuma **UI**:

- Tambahkan section baru "Add event" di `aggregate_detail.html`, berisi form
  yang sama strukturnya dengan form `add-event` di `commands.html`, tapi:
  - field `aggregate` di-render sebagai `<input type="hidden">` terisi
    `{{.FileName}}` (snake_case) — user tidak perlu isi manual, tidak bisa
    salah ketik.
  - field `event` (PascalCase) dan `fields` (list `field:type`) tetap seperti
    form yang ada.
  - `action="/commands/execute"`, `data-cmd="add-event"` — reuse endpoint dan
    JS preview yang sudah ada di `ui/static/app.js` (`cmdToArgv`), tidak perlu
    endpoint baru.
- Setelah sukses, redirect balik ke `/aggregates/{name}` (bukan ke
  `/commands/runs/{id}` generik) supaya user langsung lihat event baru muncul
  di listnya. Ini butuh sedikit penyesuaian: `handleExecute` saat ini selalu
  redirect ke `/commands/runs/{id}`; opsi termudah adalah tetap redirect ke
  situ (konsisten dengan run lain, user bisa lihat stdout/stderr generator)
  lalu link "back to aggregate" di halaman run — daripada menambah state
  redirect-tujuan yang bikin kode lebih rumit. **Rekomendasi: pertahankan
  redirect ke run-detail apa adanya**, cukup pastikan run-detail punya link
  balik ke aggregate yang jelas.
- Refactor kecil opsional: ekstrak blok `<form>` dari `commands.html` jadi
  partial `{{define "command_form"}}` yang menerima `CommandView` + peta
  hidden-field overrides, supaya form "add event" di halaman aggregate dan di
  `/commands` tidak duplikasi markup. Tidak wajib untuk MVP, tapi mengurangi
  drift kalau field command berubah nanti.

### 3.2 Hapus event dari halaman aggregate

Ini bagian yang jauh lebih besar karena **kapabilitas "hapus" belum ada di
layer manapun**. Perlu dibangun dari `injector` sampai `ui`.

#### 3.2.1 Apa yang sebenarnya harus dihapus

Kebalikan persis dari `generator.AddEvent` (`generator/event.go:81-152`), pada
dua file:

1. `domain/<aggregate>.go`:
   - `type <EventName> struct { ... }` (blok struct)
   - `func New<EventName>Event(...) <EventName> { ... }` (constructor)
   - `case "<EventName>": ... return nil` di dalam `switch` pada `Apply()`
2. `projection/<aggregate>_worker.go`:
   - `case "<EventName>": ...` di dalam `switch` pada `applyEvent()`

Plus satu kasus tepi: kalau ada file upcaster
`domain/upcast_<aggregate>_<event>.go` (dibuat oleh `esb add upcaster`) yang
mereferensikan event ini — file itu jadi tidak valid begitu struct-nya
dihapus. Lihat [4. Keputusan terbuka](#4-keputusan-terbuka) soal kebijakan ini.

#### 3.2.2 Kenapa tidak bisa pakai pendekatan string/regex sederhana

`injector` sekarang cuma py`Append`/`InjectAfterMarker`/`EnsureImport` — semua
menambah, tidak pernah menghapus. Menghapus `case "OrderPlaced":` atau
`type OrderPlaced struct` dengan `strings.Index`/regex berisiko:

- **False match pada nama yang saling substring**: `OrderPlaced` vs
  `OrderPlacedRefunded` — regex batas kata yang ceroboh bisa memotong body
  yang salah, atau `Contains` yang dipakai sebagai guard idempotensi di
  beberapa command generator (`AddAggregate`, dst.) sudah punya pola bug
  serupa yang disebut di `assesment-opus.md` (C-series "guard idempotensi
  substring").
- **Case clause tidak selalu satu baris** — bisa multi-baris (seperti
  `applyCaseTmpl`), jadi butuh tracking depth `{`/`}` yang benar (regex
  brace-matching manual, atau AST).

Karena repo ini sudah punya AST-based scanning yang solid di `inspector/scan.go`
(pakai `go/ast`+`go/format`, bukan cuma regex — lihat `extractEvents`,
`applyCaseNames`), **penghapusan sebaiknya juga AST-based**, bukan
tambal-sulam string match. Ini juga sejalan dengan rekomendasi jangka-panjang
di `assesment-opus.md` poin 10.

#### 3.2.3 Primitive baru di `injector`

Tambahkan ke `injector/tx.go` (dan versi file-langsung di `injector/marker.go`
kalau dipakai di luar `Tx`) tiga operasi berbasis `go/ast` + `go/printer`:

- `RemoveTypeDecl(path, typeName string) error` — hapus `type X struct {...}`
  top-level (dan constant/var yang cuma dipakai situ kalau ada — untuk event,
  tidak ada).
- `RemoveFuncDecl(path, funcName string) error` — hapus fungsi top-level
  (dipakai untuk `New<EventName>Event`).
- `RemoveSwitchCase(path, funcName, caseValue string) error` — cari
  `FuncDecl` bernama `funcName`, telusuri body-nya untuk `*ast.SwitchStmt`
  pertama, hapus `*ast.CaseClause` yang `case`-nya berupa string literal
  `caseValue`.

Masing-masing: parse file dengan `go/parser` (bukan string index), mutasi AST
node (`ast.Inspect`/manual walk lalu splice slice `Decls`/`Body.List`), lalu
`go/printer.Fprint` (atau `format.Node`) untuk render ulang — hasilnya
langsung digofmt sehingga konsisten dengan `writeFormatted`/`Tx.Commit` yang
sudah men-`format.Source` sebelum commit.

Semua operasi ini **stage di `Tx`** yang sama seperti `AddEvent`, supaya tetap
dapat jaminan atomic-commit yang sudah ada (`Tx.Commit` menolak menulis kalau
ada satu file yang tidak valid Go setelah perubahan — lihat
`injector/tx.go:112-145`).

Error harus eksplisit (bukan warning yang ditelan) kalau:
- type/func/case yang dicari tidak ditemukan (event sudah tidak ada / typo),
- `go/parser` gagal mem-parse file (source sudah korup / diedit manual),
- hasil akhir tidak lolos `format.Source`.

#### 3.2.4 `generator.RemoveEvent`

Fungsi baru di `generator/event.go` (atau file baru `generator/remove_event.go`
supaya `event.go` tidak terlalu gemuk):

```go
func RemoveEvent(aggregateName, eventName string) error {
    // 1. Validasi nama (sama seperti AddEvent: snake_case aggregate, PascalCase event).
    // 2. Cek event benar-benar ada di domain/<aggregate>.go (struct + Apply case) —
    //    kalau tidak ada, error jelas: "event X tidak ditemukan di aggregate Y".
    // 3. Cek file upcaster domain/upcast_<agg>_<event>.go — lihat keputusan §4.
    // 4. tx := injector.NewTx()
    //    tx.RemoveTypeDecl(domainFile, eventName)
    //    tx.RemoveFuncDecl(domainFile, "New"+eventName+"Event")
    //    tx.RemoveSwitchCase(domainFile, "Apply", eventName)
    //    tx.RemoveSwitchCase(workerFile, "applyEvent", eventName)   // kalau case ada di worker
    // 5. tx.Commit()
}
```

CLI baru: `esb delete event <aggregate> <EventName>` (`cmd/delete.go` +
`cmd/delete_event.go`, mirip pola `cmd/add.go` + `cmd/add_event.go`), didaftar
sebagai parent command `deleteCmd` di `cmd/root.go`. Tambahkan flag
`--force` di level CLI untuk power-user yang sudah tahu risikonya dan mau
skip pengecekan data (lihat §3.2.5) — dipakai kalau dijalankan langsung dari
terminal, bukan lewat UI.

Nama command: `delete` dipilih supaya simetris dengan tombol UI "Delete" dan
tidak tumpang tindih makna dengan `esb migrate` (yang juga punya konotasi
"remove source data"). Alternatif `esb remove event` juga masuk akal — lihat
[Keputusan terbuka](#4-keputusan-terbuka).

#### 3.2.5 Pengecekan data sebelum hapus (inti permintaan user)

Ini bagian yang secara eksplisit diminta: **jangan izinkan hapus tanpa cek
dulu apakah datanya sudah ada**, baik di embedded maupun di esb-server.

**Embedded (SQLite lokal) — bisa dicek akurat, murah:**

- Tambahkan ke `inspector.StorageInfo` (`inspector/storage.go`):
  ```go
  // EventCounts maps aggregate_name -> event_name -> row count, embedded mode only.
  EventCounts map[string]map[string]int
  ```
- Tambahkan `scanSQLiteEventNameCounts(dsn string) (map[string]map[string]int, bool)`
  — query baru: `SELECT aggregate_name, event_name, COUNT(*) FROM events GROUP BY aggregate_name, event_name`
  (pola query identik dengan `scanSQLiteTableCounts`, tinggal tambah kolom
  `event_name` di `SELECT`/`GROUP BY`).
- Isi di `ScanStorage` bersamaan dengan `Counts`/`SnapshotCounts` yang sudah
  ada.
- Helper `func (s StorageInfo) EventCount(aggregate, event string) int` untuk
  dipakai handler.

**esb-server (remote) — tidak ada endpoint count-by-event-name:**

Client `eventstore.Client` cuma bisa mengambil daftar event mentah
(`Events`/`EventsAll`), tidak ada agregasi count di server. Dua opsi (perlu
keputusan user, lihat §4):

- **Opsi A (best-effort, di dalam repo ini saja):** panggil `EventsAll` per
  halaman (`limit=100`) untuk aggregate ini, filter `EventName == eventName`
  di sisi client, berhenti di batas jumlah halaman/waktu (mis. maksimum 20
  halaman ~2000 event / timeout 5 detik) supaya tidak menggantung UI. Kalau
  scan berhenti karena batas (bukan karena benar-benar habis), tampilkan
  status **"belum bisa dipastikan habis — ditemukan N event sejauh ini,
  mungkin ada lebih banyak"** — bukan klaim pasti "0 event". Bisa lambat dan
  tidak lengkap untuk histori besar.
- **Opsi B (jangka panjang, di luar scope repo `esb`):** minta endpoint baru
  di server `event-sourcing-builder`
  (`GET /events/count?aggregate_name=...&event_name=...`) — repo terpisah,
  butuh koordinasi/PR di sana. `esb` baru bisa memakainya begitu tersedia.
- **Fallback minimum (kalau tidak mau implement A/B dulu):** UI cukup jujur
  menampilkan **"Mode esb-server aktif — esb tidak bisa memverifikasi data
  event ini dari sini. Cek manual di server sebelum menghapus."** dan
  mewajibkan user mencentang konfirmasi eksplisit. Ini paling murah untuk MVP
  dan tidak memberi rasa aman palsu.

Rekomendasi default kalau user tidak punya preferensi kuat: **mulai dengan
fallback minimum untuk esb-server**, dan implementasikan cek akurat penuh
untuk embedded (yang mayoritas dipakai saat development lokal — sesuai
`.env.example` default `EVENT_STORE_MODE=embedded`). Opsi A/B bisa menyusul
sebagai iterasi berikutnya.

#### 3.2.6 Alur UI

1. Di section "Events" pada `aggregate_detail.html`, tiap event dapat tombol
   **"Delete"** yang mengarah ke `GET /aggregates/{name}/events/{event}/delete`
   (route baru, method GET untuk halaman konfirmasi — bukan langsung
   menghapus).
2. Handler baru `handleDeleteEventConfirm`:
   - Validasi `{name}` dan `{event}` sama seperti `handleAggregate`.
   - Jalankan `inspector.Scan` + `inspector.ScanStorage` seperti biasa.
   - Hitung `storage.Info.EventCount(aggregate.FileName, event)` (embedded)
     atau siapkan pesan caveat esb-server (§3.2.5).
   - Render halaman baru (template baru, mis. `delete_event.html`) yang
     menampilkan:
     - nama event, aggregate, mode storage aktif,
     - **hasil cek**: jumlah event tersimpan (embedded) atau caveat
       (esb-server),
     - kalau count > 0: banner peringatan merah + checkbox wajib
       "Saya paham event ini sudah pernah tersimpan dan replay ke depan akan
       mengabaikan datanya" sebelum tombol submit aktif,
     - kalau count == 0 (atau esb-server dengan konfirmasi manual): tombol
       submit polos,
     - form `POST` ke route yang sama, di-guard `checkSameOrigin` seperti
       route mutating lain.
3. Tambahkan entry catalog baru `delete-event` di `ui/commands.go` (field:
   `aggregate` hidden = `FileName`, `event` hidden = nama event, plus field
   tersembunyi `force`/`ack` sesuai hasil checkbox) yang membangun argv
   `esb delete event <aggregate> <EventName>` — dieksekusi lewat
   `RunStore`/`ProcessRunner` yang sama persis dengan command lain (tidak ada
   jalur eksekusi baru, tetap no-shell + allowlist tertutup).
4. Setelah run selesai (redirect ke `/commands/runs/{id}` seperti alur
   command lain), sediakan link balik ke `/aggregates/{name}`.

### 3.3 Ringkasan kapabilitas baru per package

| Package | Baru |
|---|---|
| `injector` | `RemoveTypeDecl`, `RemoveFuncDecl`, `RemoveSwitchCase` (AST-based), staged lewat `Tx` yang sudah ada. |
| `generator` | `RemoveEvent(aggregateName, eventName string) error`. |
| `cmd` | `esb delete event <aggregate> <EventName>` (+ `deleteCmd` parent, didaftar di `root.go`). |
| `inspector` | `StorageInfo.EventCounts` + `scanSQLiteEventNameCounts` + helper `EventCount()`. |
| `ui` | Route `GET /aggregates/{name}/events/{event}/delete`, handler baru, template `delete_event.html`, catalog entry `delete-event`, form "Add event" ditempel di `aggregate_detail.html`, view-model tambahan di `ui/model.go` (`AggregateDetailPage` perlu field storage/count untuk ditampilkan, atau halaman delete pakai page-model sendiri — direkomendasikan page-model terpisah, `DeleteEventPage`, supaya `aggregate_detail.html` tetap ringan). |

## 4. Keputusan terbuka

Butuh konfirmasi sebelum implementasi jalan, supaya tidak salah arah:

1. **Nama command CLI**: `esb delete event` vs `esb remove event`. Rekomendasi:
   `delete` (simetris dengan tombol UI, tidak bentrok makna dengan `migrate`).
2. **Kebijakan upcaster yang menargetkan event yang mau dihapus**
   (`domain/upcast_<agg>_<event>.go`, dibuat oleh `esb add upcaster`):
   - (a) blokir penghapusan sampai upcaster dihapus manual, atau
   - (b) hapus otomatis sekalian.
   Rekomendasi: (a) — lebih aman, upcaster biasanya sengaja ditulis untuk
   memigrasi payload lama, menghapusnya otomatis berisiko silent data loss
   yang lebih halus lagi.
3. **Kedalaman cek esb-server** (§3.2.5): fallback-minimum (caveat +
   konfirmasi manual) dulu, atau langsung bangun opsi A (best-effort scan
   berbatas)? Rekomendasi: fallback-minimum dulu untuk MVP, opsi A sebagai
   iterasi berikutnya kalau memang esb-server dipakai aktif oleh tim.
4. **Redirect setelah add/delete sukses**: tetap ke `/commands/runs/{id}`
   (konsisten dengan command lain, user lihat stdout generator) dengan link
   balik ke aggregate — atau langsung redirect ke `/aggregates/{name}` dan
   sembunyikan detail run kecuali gagal? Rekomendasi: tetap ke run-detail,
   supaya konsisten dan user tetap bisa lihat error generator kalau gagal.
5. **Apakah "hapus event" juga harus punya opsi memurnikan (purge) data yang
   sudah tersimpan** (hapus baris di tabel `events`/kirim delete ke server)?
   Rekomendasi kuat: **tidak** — di luar scope permintaan ("cek dulu", bukan
   "hapus datanya juga"), dan menghapus event history event-sourced adalah
   operasi destruktif yang seharusnya tidak difasilitasi lewat tombol UI lokal
   ini. Cukup blokir/peringatkan berdasarkan cek yang ada.

## 5. Test plan

- `injector`: unit test untuk `RemoveTypeDecl`/`RemoveFuncDecl`/`RemoveSwitchCase`
  — happy path, target tidak ditemukan, source tidak valid Go, case clause
  multi-baris, dua case dengan nama mirip (`OrderPlaced` vs
  `OrderPlacedRefunded`) untuk membuktikan tidak ada false match.
- `generator`: `RemoveEvent` — mirror `generator/add_flow_test.go` tapi
  arahnya `add event` lalu `RemoveEvent`, assert file kembali (nyaris)
  sama seperti sebelum `add`, dan `go build ./...` pada temp project tetap
  sukses setelah remove. Test tambahan: event tidak ada → error jelas;
  upcaster ada → sesuai kebijakan §4.
- `inspector`: test `scanSQLiteEventNameCounts` dengan fixture SQLite berisi
  beberapa aggregate/event, termasuk kasus tabel belum ada (project baru).
- `ui`: handler test untuk route delete-confirm baru (found/not-found event,
  same-origin check di POST, count>0 vs count==0 rendering, entry catalog
  `delete-event` argv-nya benar) — mengikuti pola test yang sudah ada di
  `ui/server_test.go`.
- Manual smoke test di project nyata milik user (aggregate `academic_year`):
  tambah event baru dari halaman aggregate, verifikasi muncul; hapus event
  yang belum pernah dipakai (count 0) dan verifikasi hilang + `go build`
  masih sukses; simulasikan "event sudah dipakai" (jalankan service supaya
  ada baris di SQLite) lalu coba hapus, verifikasi warning muncul dan tidak
  bisa submit tanpa centang konfirmasi.

## 6. Fase implementasi (urutan yang disarankan)

1. **Fase 0 — Inspector**: `StorageInfo.EventCounts` + query baru + helper +
   test. Tidak menyentuh UI/generator, aman untuk merge duluan.
2. **Fase 1 — Injector**: tiga primitive AST-removal + test. Masih murni
   library, tidak ada perubahan behavior CLI.
3. **Fase 2 — Generator + CLI**: `RemoveEvent` + `esb delete event` + test
   end-to-end (add → remove → build).
4. **Fase 3 — UI**: route/handler/template baru untuk delete-confirm, catalog
   entry `delete-event`, form "Add event" di `aggregate_detail.html`, test
   handler.
5. **Fase 4 — Docs**: update `README.md` (tabel command, tabel routes UI,
   command yang didukung) dan catat kapabilitas baru di `assesment-opus.md`
   kalau perlu (opsional).

Tiap fase bisa dikerjakan (dan di-review/commit) terpisah — fase 0–2 tidak
bergantung pada UI sama sekali, jadi bisa dites lewat CLI dulu sebelum
menyentuh `ui/`.

## 7. Risiko & batasan yang perlu disadari

- Menghapus event **tidak** memurnikan data yang sudah tersimpan (lihat §4
  poin 5) — kalau event itu pernah terjadi, baris di `events` (embedded) atau
  di server (esb-server) tetap ada selamanya; yang hilang cuma
  representasi kode-nya. `Apply()` punya `default: return nil`, jadi replay
  ke depan untuk aggregate itu **diam-diam mengabaikan** event yang sudah
  dihapus definisinya — versi aggregate tetap naik dari event lain, tapi efek
  event yang dihapus tidak lagi diterapkan. Ini persis risiko yang membuat
  pengecekan sebelum hapus penting, dan kenapa peringatannya harus tegas.
- Pengecekan esb-server (kalau pakai opsi A best-effort) bisa lambat/berat
  untuk aggregate dengan histori besar, dan tidak pernah 100% memberi jaminan
  "0 event" — hanya "0 dari yang berhasil diperiksa". Harus dikomunikasikan
  apa adanya di UI, jangan diberi label "aman" yang menyesatkan.
- Snapshot (`snapshots` table / endpoint) yang dibuat dari state aggregate
  yang **sudah pernah** menerapkan event ini tetap menyimpan efeknya di field
  aggregate (JSON snapshot) — menghapus definisi event tidak mengubah snapshot
  lama. Ini konsisten dengan sifat event sourcing (state adalah hasil replay
  sampai titik snapshot), bukan bug baru, tapi baik disebutkan di UI supaya
  user tidak salah asumsi "sudah bersih total".
- Semua perubahan tetap harus lewat `injector.Tx` yang sudah ada supaya warisan
  jaminan "atomic commit, gofmt-valid, atau tidak menulis apa pun" tetap
  berlaku persis seperti alur `add` yang sudah ada — jangan menulis file
  langsung di luar `Tx` untuk fitur baru ini.
