# Assessment — esb (Event Sourcing Boilerplate)

> Ditulis oleh Claude (Opus 4.8), 2026-08-14. Berdasarkan pembacaan kode
> `generator/`, `injector/`, `inspector/`, `ui/`, `eventstore/`, `cmd/`,
> `naming/`, template, dokumentasi, plus reproduksi end-to-end (build proyek
> hasil generate). Semua temuan disertai `file:line` dan bisa direproduksi.

---

## 1. Ringkasan Eksekutif

`esb` adalah CLI Go untuk men-scaffold proyek event-sourcing yang lengkap dan
bisa dijalankan: domain aggregate, service, projection worker, repository
adapter, HTTP handler, dependency injection manual, plus mode embedded
(SQLite) ↔ esb-server (HTTP remote), migrasi dua arah, testkit Given-When-Then,
dan web UI lokal. Dokumentasinya luar biasa lengkap (README ~670 baris),
`go vet` bersih, dan `go test ./...` hijau.

**Namun ada satu cacat yang menutupi kualitas keseluruhan:** mengikuti
Quick Start di README apa adanya menghasilkan proyek yang **tidak bisa
di-compile**. Perintah `esb add handler` menulis referensi ke package dan
variabel yang tidak pernah di-inject. Ini bug rilis-blocker karena `add
handler` ada di jalur "hello world" pertama setiap pengguna baru.

**Verdict:** fondasi arsitektur dan dokumentasi sangat kuat; yang kurang adalah
**correctness di layer injeksi** dan **jaring pengaman (test end-to-end untuk
`add`, CI, guardrail)**. Perbaiki handler-wiring + tambahkan satu test yang
"generate lalu build", dan produk ini naik kelas dari "demo yang rapi" menjadi
"tool yang tepercaya".

Skor kasar per dimensi:

| Dimensi | Skor | Catatan |
|---|---|---|
| Arsitektur & desain | 9/10 | Pola dua-mode, snapshot, cursor, testkit — matang |
| Dokumentasi | 9/10 | README & AGENTS.md sangat baik; ada beberapa klaim yang overstate |
| Correctness (generator) | 4/10 | `add handler` menghasilkan proyek gagal compile |
| Keamanan (UI) | 7/10 | Allowlist argv solid; kurang guard non-loopback & header |
| Test & CI | 5/10 | Test bagus per-unit, tapi tak ada e2e untuk `add`, tak ada CI |
| Ketahanan parsing/injeksi | 4/10 | String/regex, bukan AST; error di-downgrade jadi warning |

---

## 2. Arsitektur (apa yang dibangun)

Setiap proyek hasil generate mengikuti alur:
`HTTP → Handler → Service (load→validate→store) → EventRepository → Event Store
→ Projection Worker (long-poll, cursor) → Read Model (GORM) → Query`.

Yang membuat desain ini di atas rata-rata scaffolder:

- **Dua mode transparan.** `embedded` (SQLite lokal, nol dependency eksternal)
  dan `esb-server` (HTTP + JWT ES256). Dipilih via `EVENT_STORE_MODE`, di-wire
  di `wire/wire.go`. Developer bisa langsung `make run` tanpa server.
- **Snapshot otomatis** tiap N versi + `load()` yang memuat snapshot lalu
  replay selisihnya. Gagal snapshot ≠ gagal command (snapshot murni optimasi).
- **Projection cursor** per-worker, transaksional (update cursor + read model
  dalam satu tx) → restart aman, tanpa skip/double-process.
- **Testkit Given-When-Then** dengan `FakeStore` in-memory yang punya semantik
  optimistic-concurrency identik dengan store asli → test menjalankan
  `load()/store()` sungguhan, bukan mock yang disederhanakan.
- **Migrasi dua arah** dengan `expected_version` semantics (idempoten,
  resumable) + state file untuk UI.
- **`AGENTS.md`** yang di-generate untuk mengorientasikan asisten AI — detail
  yang jarang ada di tool sejenis.

Fondasi ini bagus. Masalahnya ada di lapisan bagaimana kode di-*rakit*, bukan
pada polanya.

---

## 3. Temuan Kritis (bug, prioritas turun)

### 🔴 C1 — `esb add handler` menghasilkan proyek yang gagal compile

**Ini blocker.** Reproduksi persis mengikuti pola README:

```bash
esb init shop --here
esb add aggregate order
esb add event order OrderPlaced amount:int64
esb add handler place_order --aggregate order
go build ./...
```

Hasil:

```
wire/wire.go:56:25: undefined: handler
wire/wire.go:110:23: undefined: handler
wire/wire.go:110:52: undefined: orderSvc
```

Tanpa langkah `add handler`, proyek **compile bersih**. Jadi kerusakan
terisolasi persis di `add handler`.

**Akar masalah** (`generator/handler.go:42-56`): saat injeksi ke `wire/wire.go`,
`AddHandler` menulis:
- field `PlaceOrderHandler *handler.PlaceOrderHandler`, dan
- init `placeOrderHandler := handler.NewPlaceOrderHandler(orderSvc)`

tetapi **tidak pernah**:
1. meng-inject import `"<module>/server/handler"` ke marker
   `// esb:inject:wire-imports` (marker-nya ada di `wire_wire.go.tmpl`, tapi
   tak ada `EnsureImport`/injeksi ke sana) → `undefined: handler`.
2. membuat instance service `orderSvc := service.NewOrderService(...)` (dan
   import `"<module>/service"`). Baik `add aggregate` maupun `add handler`
   tidak pernah menaruh service ke dalam builder `App` → `undefined: orderSvc`.

Ketiga pemanggilan `InjectAfterMarker` di `handler.go:46,50,53` **membuang
return value** — jadi seandainya marker hilang pun, perintah tetap lapor sukses.

**Dampak:** setiap pengguna baru yang mengikuti Quick Start mendapati `make run`
gagal build di langkah pertama. Ini menghapus kesan "satu command langsung
jalan" yang dijanjikan README.

**Perbaikan:** di `AddHandler`, tambahkan (a) `EnsureImport` untuk package
`handler` dan `service`, (b) injeksi `service.New<Agg>Service(repo)` ke
`app-init` bila belum ada (idealnya `add aggregate` yang menaruh service +
provider-nya, lalu handler tinggal mereferensikan), dan (c) **periksa error**
dari setiap injeksi, jangan dibuang.

---

### 🔴 C2 — Tidak ada test yang mem-build proyek setelah `add *`

Ini penyebab C1 lolos. Satu-satunya test end-to-end yang benar-benar
meng-compile hasil generate adalah `generator/project_test.go:11`
(`TestInitProject_GeneratedEmbeddedProjectCompiles`) — dan itu **hanya**
menguji `init`. Tidak ada satu pun test yang menjalankan
`add aggregate/event/handler/query/projection` lalu memastikan proyeknya
masih `go build`.

`AddAggregate`, `AddEvent`, `AddHandler`, `AddQuery`, `AddProjection`,
`ParseFields`/`validType` (`generator/event.go:15-41`), dan **seluruh package
`injector`** (`injector/marker.go`) — nol unit test. Padahal `injector`
adalah kode paling rapuh (splicing berbasis byte-offset, guard substring).

**Perbaikan (paling berdampak):** satu table-test yang mensimulasikan alur
lengkap `init → add aggregate → add event → add handler → add query → add
projection` di temp dir lalu `go build ./...`. Test ini akan langsung merah
karena C1, dan menjaga agar tidak terulang.

---

### 🟠 C3 — `ToPlural` salah untuk pola vokal+`y` → nama tabel rusak

`naming/conv.go:81` mengubah semua akhiran `y` menjadi `ies`, padahal doc
comment-nya sendiri menyatakan "vowel+y → ys". Akibatnya:

```
gateway  -> gatewaies   (seharusnya gateways)
day      -> daies
key      -> keies
survey   -> surveies
```

Terbukti di artefak: `esb add aggregate gateway` menghasilkan
`func (GatewayRow) TableName() string { return "gatewaies" }`.

**Dampak:** nama tabel read-model yang aneh/salah untuk aggregate yang berakhir
vokal+`y` (gateway, survey, journey, day, category **benar** tapi day salah).
Sekali dipakai di produksi, nama tabel sulit diubah.

**Perbaikan:** cek huruf sebelum `y` — konsonan+`y`→`ies`, vokal+`y`→`ys`.
Tambahkan unit test untuk `naming` (saat ini nol test).

---

### 🟠 C4 — `esb init` gagal di lingkungan non-interaktif (CI/script)

`esb init <name>` memunculkan prompt interaktif ("Create new folder / use
current"). Di lingkungan tanpa TTY (pipe, CI, Dockerfile, `make` di runner)
ia langsung error:

```
Choose [1/2] (default 1): Error: read prompt: EOF
```

Ada flag `--here`, tapi tak ada flag untuk memilih "buat folder baru" secara
non-interaktif (mis. `--new` atau deteksi non-TTY → pakai default). README
Quick Start (`esb init toko-online`) karena itu tidak bisa dijalankan di CI apa
adanya.

**Perbaikan:** kalau stdin bukan TTY, jangan prompt — pakai default (buat
folder) atau hormati flag eksplisit. Prompt hanya saat interaktif.

---

## 4. Temuan Kualitas Kode

### Generator / Injector (`generator/`, `injector/`)

- **Injeksi berbasis string, bukan AST.** `injector/marker.go` mencari marker
  dengan `strings.Index` lalu menyisipkan pada byte-offset. Tidak ada validasi
  sintaks Go pada posisi sisip.
- **Error injeksi di-downgrade jadi `warn` atau dibuang** di hampir semua
  `add` (`aggregate.go:52,61…`, `projection.go:45…`, `event.go:103…`), dan
  **dibuang total** di `handler.go:46,50,53`. Perintah bisa lapor sukses padahal
  wiring tidak masuk (ini yang menutupi C1).
- **`go/format.Source` error diabaikan diam-diam** (`render.go:33`,
  `marker.go:83-88`) → Go tak valid bisa tertulis ke disk tanpa sinyal.
- **Guard idempotensi = substring identifier**, rawan false-positive: `add
  query Order` setelah `OrderItem` ada akan salah dianggap "sudah ada"
  (`query.go:31`); `EnsureImport` mencocokkan substring `"time"`, dst.
- **Guard kasar untuk banyak injeksi sekaligus** (`aggregate.go:60`,
  `handler.go:44`, `projection.go:54`): satu guard menggerbang tiga splice.
  Bila run pertama gagal di tengah, run kedua ter-guard → proyek
  **separuh-terwire permanen**.
- **`[:1]` tanpa validasi** (`aggregate.go:26`, `event.go:53`) → panic pada
  input kosong. Tidak ada validasi nama identifier di layer generator.
- **Duplikasi:** blok injeksi wire.go nyaris identik di tiga file; helper
  `lcFirst` vs inline `strings.ToLower(x[:1])+x[1:]`.
- **Doc drift:** komentar `EnsureImport` (`marker.go:38-40`) mengklaim
  mencocokkan segmen terakhir path, padahal mencocokkan path lengkap.

### Inspector (`inspector/`)

- **Parsing 100% regex + brace-scan manual, bukan `go/ast`** (17 pola
  `regexp.MustCompile` di `scan.go`). Ini keterikatan erat (lockstep) dengan
  teks output generator:
  - Deteksi event bergantung string komentar persis `// OrderPlaced event.`
    (`scan.go:260`) — ubah wording template, event hilang **tanpa error**.
  - Field event butuh spacing gofmt + json tag wajib (`scan.go:274`).
  - Query harus signature satu baris (`scan.go:669`) — kalau di-wrap gofmt,
    tak terbaca.
  - Mode gagal adalah **silent-empty** (model kosong, bukan error) → sulit
    disadari saat drift.
- Kredit: sudah ada brace-depth tracking yang benar (`trimToMatchingBrace`,
  `extractSwitchAggregateNames`) + regression test non-gofmt (`scan_test.go:420`).
  Tapi ketergantungan wording komentar **tidak ada test**-nya.

### UI (`ui/`)

- **Eksekusi command sangat solid.** Allowlist tertutup (`commands.go:54`),
  tiap command membangun `argv` eksplisit, `exec.CommandContext` tanpa shell,
  validasi per-field menolak metakarakter (`commands.go:184-336`), handler
  menolak field yang tak ada di katalog (`handlers.go:240-248`). Ini pertahanan
  berlapis yang bagus.
- **Dead code:** `var _ = url.Parse` (`storage_handlers.go:306`) — `net/url`
  tak dipakai lagi di file itu; hapus.
- **Doc drift:** komentar `scanSQLiteTableCounts` (`storage.go:201-204`)
  menyebut klausa `_pragma` yang mematikan journal, padahal kode hanya menambah
  `mode=ro`.
- **`RunStore` tak pernah evict** (`commands.go:613`): setelah 1000 run,
  `ErrStoreFull` permanen sampai restart → eksekusi command "terkunci".
  Sebaiknya ring-buffer / evict run yang sudah selesai.
- **Child process tak dibunuh saat shutdown**: `runContext()` pakai
  `context.Background()` (`server.go:148`), jadi graceful-shutdown `esb ui` tak
  membatalkan generator yang sedang jalan (hanya timeout 5 menit).

---

## 5. Keamanan (UI lokal)

Postur dasar bagus (loopback default, no-shell, allowlist), tapi:

- **Tidak ada autentikasi.** Proteksi hanya (a) bind loopback + (b) Origin
  check. Origin check itu anti-CSRF, **bukan auth** — `curl -H "Origin:
  http://127.0.0.1:8787"` lolos. Komentar `handlers.go:331` yang bilang menolak
  "curl, etc." menyesatkan.
- **`--addr` menerima alamat apa pun tanpa peringatan** (`cmd/ui.go:108`).
  Bind ke `0.0.0.0` + tanpa auth = RCE dalam batas allowlist + bisa migrasi ke
  URL yang dikuasai penyerang. Perlu guard/warning kalau bind non-loopback.
- **Header minim** (`server.go:135`): hanya `X-Content-Type-Options` +
  `Cache-Control`. Tak ada CSP / `X-Frame-Options` → clickjacking UI admin
  lokal mungkin. Tambahkan CSP ketat (semua aset sudah di-embed, jadi CSP
  `default-src 'self'` mudah).

---

## 6. Higiene Proyek (esb sendiri)

- **Tidak ada CI.** Tak ada `.github/workflows`. Untuk tool yang
  di-`go install` publik, minimal CI `go build && go vet && go test ./...`
  (idealnya + test e2e C2) wajib.
- **Tidak ada file LICENSE** walau README menulis "MIT". Tambahkan `LICENSE`
  agar klaim itu sah secara hukum.
- **Tidak ada `esb version`.** CLI yang didistribusikan via `go install @latest`
  sebaiknya bisa melaporkan versinya (via `-ldflags`/`debug.ReadBuildInfo`)
  untuk bug report.
- **Perubahan besar belum di-commit.** `git status` menunjukkan template inti
  (eventstore, service, wire) + 4 template baru (testkit, fake_store, scenario
  test, AGENTS.md) masih modified/untracked — pengujian di atas memakai working
  tree ini. Pastikan masuk satu commit yang koheren.

---

## 7. Saran Peningkatan Produk (roadmap berprioritas)

**Sekarang (rilis-blocker):**
1. Perbaiki **C1** — `add handler` harus meng-inject import `handler`+`service`
   dan instance service; periksa error injeksi.
2. Tambah **C2** — test "generate → `go build ./...`" untuk alur `add` lengkap.
3. Perbaiki **C3** (`ToPlural`) + tambah unit test `naming`.
4. Perbaiki **C4** — `init` non-interaktif saat bukan TTY.
5. Tambah **CI** (build+vet+test) dan file **LICENSE**.

**Berikutnya (ketahanan):**
6. Berhenti membuang error injeksi; jadikan `add *` **transaksional** — kalau
   satu injeksi gagal, batalkan semua perubahan file (tulis ke temp lalu
   rename, atau kumpulkan diff dan commit sekali) agar tak ada proyek
   separuh-terwire.
7. Ganti guard idempotensi substring dengan pengecekan yang berbatas
   (mis. token/word-boundary atau parse ringan) untuk hilangkan false-positive
   `OrderItem` vs `Order`.
8. Tambah `esb version`.
9. UI: guard/warning bind non-loopback; tambah CSP + `X-Frame-Options`; evict
   run lama di `RunStore`.

**Strategis (fondasi jangka panjang):**
10. **Migrasi injeksi & inspeksi ke `go/ast` + `go/format`.** Ini menghapus
    seluruh kelas kerapuhan: injeksi berbasis-AST tak bisa menaruh kode di
    posisi tak-valid, dan inspector berbasis-AST tak peduli spacing gofmt atau
    wording komentar. Menghilangkan keterikatan lockstep antara template ↔
    regex inspector yang saat ini gagal diam-diam.
11. Atau, jika ingin tetap ringan: buat **golden test** yang mengikat output
    template ↔ regex inspector, sehingga perubahan wording template yang
    memecah parser langsung ketahuan.
12. Perkaya validasi input CLI: tolak nama aggregate/handler kosong/non-snake,
    event non-Pascal, dengan pesan jelas (bukan panic `[:1]`).
13. Pertimbangkan integrasi Google Wire sungguhan atau generator `wire_gen`,
    atau setidaknya jadikan wiring service otomatis saat `add aggregate`
    (menyelesaikan akar C1 sekaligus).

---

## 8. Kesimpulan

`esb` adalah **ide bagus yang dieksekusi dengan desain matang dan dokumentasi
kelas satu**, tetapi tersandung di eksekusi lapisan injeksi kode. Pola event
sourcing yang dihasilkan (dua-mode, snapshot, cursor, testkit, migrasi) sudah
setara tool produksi. Yang menahannya adalah **correctness**: perintah yang ada
di jalur pertama pengguna (`add handler`) memecah build, dan tidak ada jaring
pengaman (test e2e untuk `add`, CI) yang menangkapnya.

Kabar baiknya: daftar "sekarang" di atas kecil dan terarah. Menyelesaikan C1–C5
mengubah esb dari "scaffolder yang demo-nya bagus tapi rapuh saat dipakai
sungguhan" menjadi "scaffolder yang bisa diandalkan pada percobaan pertama".
Setelah itu, investasi ke AST (poin 10) adalah pembeda jangka panjang yang
membuat generator dan inspector berhenti pecah diam-diam saat template
berkembang.
