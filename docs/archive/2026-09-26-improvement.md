**Klasifikasi: INTERNAL**

# Improvement — 26 September 2026

Daftar perbaikan hasil review folder `esb` (26 Sep 2026). Baseline sebelum
dikerjakan: `go vet` bersih, `go test ./...` hijau, 8 file belum gofmt.

Status: ⬜ belum · 🔄 dikerjakan · ✅ selesai · ⏸️ butuh keputusan user

## Ringkasan hasil (26 Sep 2026)

- **14/14 task awal selesai**, plus 1 temuan baru yang sudah diperbaiki (#15)
  dan 1 temuan baru yang menunggu keputusan (#16). Tag rilis pertama (#14)
  juga menunggu keputusan user.
- Akhir: `gofmt -l .` kosong, `go vet` bersih, `go mod tidy` tanpa diff,
  `go test -race ./...` hijau, `govulncheck` bersih untuk esb **dan** proyek
  hasil generate.
- Setiap perbaikan perilaku punya test regresi yang **dibuktikan gagal di
  kode lama** lalu lulus di kode baru (kecuali #7 dan #8, yang API atau
  pesannya memang baru).
- Bug lama yang ditemukan selama pengerjaan (di luar review awal): readCursor
  replay dari 0 saat error DB (#1), write konkuren SQLite gagal massal (#2),
  `make keygen` menimpa kunci (#6), field duplikat merusak build dan error
  CLI tercetak ganda (#8), file jadi 0600 (#9), `--dry-run` fiktif dan anchor
  rusak di README (#10), dependency rentan GO-2026-5970/GO-2026-5024 di
  proyek hasil generate (#13), `make migrate-*` tidak memigrasi (#15).
- Koreksi: dugaan `private.pem` world-readable (#6) tidak terbukti di macOS.
- Sudah di-commit ke `main` (1 Okt 2026) sebagai 10 commit tematik, mulai
  dari `fix(generator): retry failed projection events instead of skipping them`.

| # | Prioritas | Task | Status |
|---|---|---|---|
| 1 | P1 | Projection worker tidak boleh melewati event yang gagal di-apply | ✅ |
| 2 | P1 | Proyek hasil generate harus jalan tanpa CGO (driver SQLite pure-Go) + write konkuren tidak `SQLITE_BUSY` | ✅ |
| 3 | P2 | `http.Server` hasil generate: timeout + shutdown ber-deadline + tunggu worker | ✅ |
| 4 | P2 | Batasi ukuran request body (`http.MaxBytesReader`) di handler hasil generate | ✅ |
| 5 | P2 | Jangan bocorkan `err.Error()` pada respons 5xx | ✅ |
| 6 | P2 | `make keygen`: `private.pem` 0600 + tidak menimpa kunci yang ada | ✅ |
| 7 | P3 | UI: command yang berjalan ikut dibatalkan saat `esb ui` shutdown | ✅ |
| 8 | P3 | Validasi nama field (keyword Go + duplikat) & pesan error CLI | ✅ |
| 9 | P3 | `Tx.Commit`: rollback kalau penulisan gagal di tengah + mode file 0644 | ✅ |
| 10 | P4 | README + AGENTS.md disinkronkan dengan kode | ✅ |
| 11 | P4 | Rapikan dokumen perencanaan ke `docs/`, tandai status assessment lama | ✅ |
| 12 | P4 | Tambah `.gitignore` di root repo | ✅ |
| 13 | P4 | CI: cek gofmt, `govulncheck`, `-race`; gofmt; **dependency proyek hasil generate bebas vuln** | ✅ |
| 14 | P4 | Versi rilis: proses tag semver + workflow rilis | ✅ (tag pertama ⏸️) |
| 15 | P1 | *(temuan baru)* `make migrate-to-esb/embedded` menyalakan server, bukan migrasi | ✅ |
| 16 | P3 | *(temuan baru)* Handler dari `esb add handler` tidak bisa di-route (hint `undefined: app`) | ⏸️ |

---

## Detail

### 1. Projection worker melewati event gagal — ✅

**Masalah.** Loop `for _, e := range batch` hanya me-log error `applyEvent` lalu
lanjut ke event berikutnya. Batch `[e1, e2, e3]`: `e2` gagal (tx rollback, cursor
tetap di `e1`), `e3` sukses → cursor lompat ke `e3`; `e2` hilang permanen dari
read model. Untuk outbox: integration event tidak pernah terkirim.

**Temuan tambahan saat dikerjakan.** `readCursor()` mengabaikan error DB dan
mengembalikan `0` → error DB sesaat membuat worker me-replay seluruh stream dari
awal (projection yang tidak idempoten bisa double-apply).

**Perbaikan** (7 template: `projection_worker`, `projection_multi_worker`,
`recipe/{crud,ledger,saga,sm,outbox}_worker`):
- Saat `applyEvent` gagal: log, tunggu 3 detik, `break` dari batch → iterasi
  berikutnya fetch ulang dari cursor, jadi event yang sama di-retry.
- `readCursor()` sekarang `(int64, error)`; hanya `gorm.ErrRecordNotFound` yang
  berarti `0`. Error lain → retry, bukan replay dari awal.
- `time.Sleep` diganti method `wait(ctx, d)` yang bisa dibatalkan → shutdown
  tidak tertahan 3 detik.
- Semua helper per-tipe worker (bukan helper bersama) supaya `esb add ...` baru
  tetap compile di proyek yang di-generate versi lama.

**Trade-off (disengaja).** Event yang *selalu* gagal akan memblokir worker
tersebut (terlihat di log tiap 3 detik), bukan dilewati diam-diam. Untuk
event-sourcing, projection yang berhenti lebih aman daripada read model yang
bolong. Penanganan: perbaiki kode projection atau tambah upcaster.

**Verifikasi.**
- Test baru `generator/projection_retry_test.go`: generate proyek, simpan
  `[ok, bad, ok]`, jalankan worker, assert cursor berhenti di event pertama.
- Template lama: **FAIL** `cursor = 3, want 1: the failed event was skipped`
  (bug terbukti). Template baru: **PASS**.
- `go test ./...` hijau.

### 2. Proyek hasil generate butuh CGO — ✅

**Masalah.** `gorm.io/driver/sqlite` → `mattn/go-sqlite3` (cgo). Build
`CGO_ENABLED=0` sukses, tapi runtime gagal:
`go-sqlite3 requires cgo to work. This is a stub` (sudah direproduksi).

**Temuan tambahan saat dikerjakan (bug lama, bukan regresi).** Write konkuren
ke SQLite di mode embedded gagal massal. Stress test 16 goroutine × 25
`StoreAtomic`:
- driver lama (mattn, cgo): **325/400 gagal** `database is locked`
- driver baru tanpa opsi: 375/400 gagal `SQLITE_BUSY`
- driver baru + `busy_timeout` saja: 350/400 masih gagal

Penyebab: transaksi *deferred* yang membaca lalu menulis (cek versi di
`StoreAtomic`, upsert projection) tidak bisa menunggu busy timeout saat
meng-upgrade lock — SQLite langsung menolak untuk menghindari deadlock.
Artinya request HTTP paralel di mode embedded bisa dapat 500.

**Perbaikan.**
- `go.mod.tmpl`: `gorm.io/driver/sqlite v1.6.0` → `github.com/glebarez/sqlite v1.11.0`
  (pure-Go, berbasis `modernc.org/sqlite`; versi diverifikasi via `go list -m -versions`).
- `projection_db.go.tmpl`: DSN ditambah `_pragma=busy_timeout(5000)` dan
  `_txlock=immediate` lewat `sqliteDSN()` (tidak menimpa kalau DSN sudah
  mengatur sendiri). Hasil: **0/400 gagal** (3× run).
- `recipe/outbox_test.go.tmpl` dan fixture `inspector/scan_test.go` ikut
  pindah import.

**Trade-off.** `_txlock=immediate` men-serialisasi semua transaksi tulis
(SQLite memang single-writer); read di luar transaksi tidak terpengaruh.
Opsional ke depan: `journal_mode(WAL)` untuk reader yang lebih konkuren
(belum diaktifkan supaya perilaku file DB tidak berubah).

**Verifikasi.**
- Test runtime existing (`...StartsWithoutKey`, `...LocalStoreRejectsWrongExpectedVersion`)
  sekarang dijalankan dengan `CGO_ENABLED=0`.
- Test baru `TestInitProject_EmbeddedStoreHandlesConcurrentWrites`
  (`CGO_ENABLED=0`): PASS; dengan opsi DSN dimatikan: FAIL 375/400 (kontrol).
- Smoke test manual: proyek + recipe ledger, build `CGO_ENABLED=0`, `/health`
  OK, `go.sum` tanpa `go-sqlite3`.
- `go test ./...` hijau.

### 3. `http.Server` tanpa timeout — ✅

**Risk area:** availability (slowloris). `main.go.tmpl` tidak set timeout;
`Shutdown` tanpa deadline dan worker tidak ditunggu.

**Perbaikan.**
- `main.go.tmpl`: `ReadHeaderTimeout 5s`, `ReadTimeout 15s`, `WriteTimeout 30s`,
  `IdleTimeout 60s`.
- Shutdown: `srv.Shutdown` dengan deadline 10 detik; `main` menunggu drain
  selesai (`shutdownDone`) lalu `wg.Wait()` untuk semua worker
  (`sync.WaitGroup.Go`, Go ≥1.25 — sesuai `go 1.27.0` di go.mod hasil generate).
  Sebelumnya proses bisa keluar sebelum request in-flight selesai.
- `recipe/outbox_publisher.go.tmpl`: `time.Sleep` → `wait(ctx, d)` yang bisa
  dibatalkan (supaya `wg.Wait()` tidak tertahan back-off).
- Struktur blok `workers := []projection.Worker{ // esb:inject:projection-workers }`
  dipertahankan → `inspector.scanMain` dan injeksi `add aggregate/projection`
  tetap jalan.

**Verifikasi.**
- Test baru `generator/server_runtime_test.go`: build app hasil generate
  (`CGO_ENABLED=0`), jalankan sungguhan, kirim request dengan header setengah
  jadi → server memutus (±5s); lalu `SIGINT` → exit 0.
- Template lama: **FAIL** `server kept a half-sent request open for 12s`.
  Template baru: **PASS**.
- `go test ./...` hijau.

### 4. Request body tak dibatasi — ✅

**Risk area:** input validation. Handler recipe decode JSON langsung dari
`r.Body` tanpa batas ukuran.

**Perbaikan.**
- Helper baru `decodeJSON(w, r, &req) bool` di `server/handler/response.go`
  (template `recipe/response_decode.go.tmpl`): `http.MaxBytesReader` 1 MiB
  (`maxBodyBytes`), balas **413** kalau kebesaran, **400** kalau JSON rusak.
- Semua handler recipe (`crud`, `ledger`, `sm`, `saga`) memakai `decodeJSON`;
  import `encoding/json` yang tak terpakai dihapus.
- Skeleton `handler.go.tmpl` (`esb add handler`): contoh komentar sekarang
  memakai `http.MaxBytesReader`.
- **Kompatibilitas proyek lama:** kalau `response.go` sudah ada tapi belum punya
  `decodeJSON`, `ensureResponseHelper` menambahkannya (+ import `errors`) dalam
  transaksi yang sama — recipe baru tidak memecah build proyek lama.

**Verifikasi.**
- `TestRecipeHandlers_BodyLimitAndErrorMasking`: body 2 MiB → 413.
  Template lama: **FAIL** `status = 201, want 413` (body 2 MiB diterima penuh).
- `TestAddCRUD_UpgradesLegacyResponseHelper`: `response.go` versi lama +
  `AddCRUD` → tepat satu `decodeJSON`, `go build` sukses.

### 5. Kebocoran error internal — ✅

**Risk area:** error handling / information disclosure. `writeError` mengirim
`err.Error()` apa adanya, termasuk untuk 500 (error DB/internal).

**Perbaikan.** `writeError`: status ≥ 500 → error di-log server-side
(`log.Printf`), client hanya menerima `http.StatusText` (mis.
`"Internal Server Error"`). 4xx tetap mengirim pesan asli (client perlu tahu
apa yang salah).

**Batasan.** Proyek lama yang sudah punya `response.go` tidak diubah
`writeError`-nya secara otomatis (hanya `decodeJSON` yang ditambahkan) —
perlu disalin manual dari template kalau ingin perilaku baru.

**Verifikasi.**
- Test generated: repo yang gagal dengan pesan `open /srv/secret/app.db:
  permission denied` → respons 500 tidak mengandung `secret`; 400 tetap
  membawa detail error decode.
- Template lama: **FAIL** `500 response leaks internal error:
  {"error":"load snapshot: open /srv/secret/app.db: permission denied"}`.
- `go test ./...` hijau.

### 6. `make keygen` — ✅

**Risk area:** secrets management.

**Koreksi temuan awal.** Dugaan "`private.pem` world-readable" (ASSUMPTION di
review) **tidak terbukti di macOS**: LibreSSL `openssl ecparam -genkey -out`
sudah membuat file 0600 walau `umask 022`. Perilaku OpenSSL di Linux:
unverified.

**Temuan nyata saat dikerjakan.** `make keygen` kedua kali **menimpa
`private.pem` diam-diam** → kunci lama hilang dan akses ke ESB server putus
sampai `PUBLIC_KEY` baru dipasang.

**Perbaikan** (`makefile.tmpl`):
- `umask 077` pada baris yang membuat `private.pem` — menjamin 0600 terlepas
  dari build openssl (defense-in-depth).
- Guard: kalau `private.pem` sudah ada → pesan "hapus dulu kalau mau rotasi
  kunci", exit 1, file tidak disentuh.

**Verifikasi.**
- Test baru `generator/keygen_test.go` (skip kalau `make`/`openssl` tak ada):
  `umask 022 && make keygen` → mode 600; run kedua → gagal & isi kunci sama.
- Template lama: **FAIL** `second make keygen succeeded, want refusal to overwrite`.
- `go test ./...` hijau.

### 7. UI run tidak dibatalkan saat shutdown — ✅

`ui/server.go` `runContext()` mengembalikan `context.Background()`, padahal
komentarnya bilang shutdown membatalkan run. Child process jalan terus
sampai timeout 5 menit.

**Perbaikan.**
- `ui.Server` punya `runCtx` (dibuat di `NewServer`) sebagai parent semua run;
  method baru `Close()` membatalkannya lalu menunggu run tercatat
  (`RunStore.Wait()`, goroutine `execute` dilacak `sync.WaitGroup`).
- `cmd/ui.go`: `defer srv.Close()` → dijalankan setelah `httpSrv.Shutdown` di
  semua jalur keluar.
- Run yang dibatalkan berstatus `RunFailed` dengan pesan
  `cancelled: esb ui is shutting down` (bukan "exit code -1").
- `ExecRunner`: `cmd.WaitDelay = 5s` supaya grandchild yang mewarisi pipe
  stdout/stderr tidak membuat shutdown menggantung.
- Komentar `runContext()` disesuaikan dengan perilaku sebenarnya.

**Verifikasi.**
- Test baru `ui/close_test.go`: runner yang blok sampai ctx batal → `Close()`
  selesai < 5s, status `RunFailed` + pesan cancelled.
- `go test -race ./ui/ ./cmd/` hijau.

### 8. Nama field keyword Go — ✅

`esb add event order X type:string` gagal dengan pesan membingungkan
`expected ')', found 'type'` (tidak merusak file karena `Tx`, tapi UX buruk).

**Probe empiris sebelum menulis validasi** (build proyek hasil generate):

| Input | Hasil sebelum fix |
|---|---|
| field `string:string bool:bool` | aman (compile) |
| field `placed_at` | aman |
| recipe statemachine `--states open,default,go` | aman |
| field `type:string` di `add event` | error parse membingungkan |
| **field duplikat `amount:int64 amount:string`** | **kode ditulis & build rusak** (`Amount redeclared`) — `Tx` hanya menangkap error sintaks, bukan error tipe |

**Perbaikan.**
- `validateFieldName` (`generator/validate.go`): tolak nama yang bentuk
  lower-camel-nya keyword Go (`token.IsKeyword`); nama multi-segmen seperti
  `type_id` tetap boleh.
- `ParseFields`: tolak dua argumen yang menghasilkan nama field Go yang sama.
- Temuan UX terkait: setiap error CLI tercetak **dua kali** plus seluruh teks
  usage cobra. `cmd/root.go`: `SilenceErrors`/`SilenceUsage`, error dicetak
  sekali sebagai `Error: ...` + petunjuk `Run 'esb add event --help' for usage.`

Contoh sekarang:
```
Error: invalid field name "type" — "type" is a Go keyword; use a more specific name, e.g. type_name
Error: duplicate field "amount:string" — "amount:int64" already defines Go field Amount
```

**Verifikasi.** Unit test baru `TestParseFields_RejectsKeywordFieldNames` dan
`TestParseFields_RejectsDuplicateFields`; cek manual CLI; `go test ./...` hijau.

### 9. `Tx.Commit` tidak rollback — ✅

Validasi all-or-nothing, tapi penulisan tidak: kalau file ke-2 gagal ditulis,
file ke-1 sudah ter-rename.

**Temuan tambahan saat dikerjakan (bug lama).** `atomicWrite` memakai
`os.CreateTemp` (mode 0600) lalu rename → **setiap file yang disentuh
generator jadi 0600**. Contoh: `main.go`, `wire/wire.go`, `projection/db.go`
0644 setelah `init`, berubah 0600 setelah `esb add aggregate`.

**Perbaikan** (`injector/tx.go`):
- Sebelum menulis, `Commit` men-snapshot isi + permission tiap file target.
  Kalau penulisan ke-*n* gagal, file 1..*n*-1 dikembalikan (file lama
  di-restore, file baru dihapus). Kalau rollback juga gagal, error-nya
  menyebut "project may be partially written".
- `atomicWrite(path, data, perm)`: `Chmod` temp file sebelum rename —
  file lama mempertahankan mode-nya, file baru 0644.
- Batasan: direktori baru yang sempat dibuat `MkdirAll` tidak dihapus saat
  rollback (kosong, tidak memengaruhi build).

**Verifikasi.**
- `injector/tx_rollback_test.go` — sebelum fix **4 assertion FAIL**:
  file lama berisi `"original\nappended\n"`, file baru masih ada,
  `run.sh` 600 (harusnya 755), `new.txt` 600 (harusnya 644). Sesudah: PASS.
- Cek manual: `init` + `add aggregate` → semua file 0644.
- `go test ./...` hijau.

### 10. README drift — ✅

**Dicocokkan dengan kode, bukan ingatan.** Temuan tambahan selama pengecekan:
- Section **"Dry Run"** mendokumentasikan flag `--dry-run` yang **tidak ada**
  di kode → dihapus.
- Link `[Migrasi ke ESB Server](#migrasi-ke-esb-server)` mengarah ke section
  yang **tidak ada** → section baru ditulis (sekaligus memicu temuan #15).
- `esb migrate`, `esb show storage`, `esb version`, route `/flow` belum
  terdokumentasi.
- "Jika marker tidak ditemukan, esb akan print kode yang perlu ditambahkan"
  → salah; sebenarnya command gagal dan tidak menulis apa pun (diverifikasi).
- Pohon file `init` usang (`wire/providers.go` tidak ada; `upcast.go`,
  `fake_store.go`, `testkit/`, `projection/worker.go`, `repository.go` belum
  tercantum).

**Perubahan README.**
- Intro: DI manual di `wire/wire.go` (bukan Google Wire) + SQLite pure-Go.
- Quick Start & Contoh Alur Lengkap: `make wire` dihapus.
- Pohon file `init`, efek `add aggregate` / `add handler` diperbarui
  (termasuk catatan bahwa hint route belum bisa di-uncomment — lihat #16).
- `make keygen`: 0600 + tidak menimpa kunci.
- `add event`: aturan penolakan nama field (keyword/duplikat).
- Recipe: batas body 1 MiB/413 dan masking error 5xx.
- Section baru: `esb show storage`, `esb migrate`, `esb version`,
  **Migrasi ke ESB Server** (alur 4 langkah, `FORCE=1`, override `.env`).
- `esb ui`: tabel command 16 entri (dari allowlist `ui/commands.go`),
  route `/flow`.
- Projection Cursor: semantik retry (worker berhenti di event gagal).
- Injection Points: marker yang benar-benar ada di template + perilaku
  transaksional saat marker hilang.

**Template `AGENTS.md`** (ikut di-generate ke tiap proyek): catatan pure-Go
SQLite, `applyEvent` wajib mengembalikan error (worker retry), dan perilaku
`make migrate-*` yang baru.

**Verifikasi.** Semua anchor internal README resolve (dicek skrip);
`grep` tidak lagi menemukan `make wire`, `Google Wire`, `providers.go`,
`dry-run`; `go test ./...` hijau.

### 11. Dokumen perencanaan berserakan — ✅

`assesment-opus.md` (usang), `plan.md`, `code-generation-idea.md`,
`plan-ux-aggregate-page.md`, `.hermes/` di root.

**Perubahan.**
- `assesment-opus.md` — sudah dihapus user di commit `6e6f69a`.
- Dipindah dengan `mv` biasa (git index tidak disentuh; `git add -A` nanti
  akan mendeteksi rename):

| Dari | Ke | Status |
|---|---|---|
| `plan.md` | `docs/archive/2026-07-10-initial-plan.md` | sudah diimplementasikan |
| `.hermes/plans/2026-07-24_113825-esb-ui-web-based-ui.md` | `docs/archive/2026-07-24-esb-ui-web-based-ui.md` | sudah diimplementasikan |
| `plan-ux-aggregate-page.md` | `docs/archive/2026-08-15-plan-ux-aggregate-page.md` | sudah diimplementasikan (header dokumen sendiri menyatakan ✅) |
| `code-generation-idea.md` | `docs/ideas/code-generation-idea.md` | sebagian jadi; `inventory`, `tally` belum |

- `docs/README.md` baru: indeks + status per dokumen.
- Folder `.hermes/` kosong → dihapus.
- Tidak ada kode/dokumen yang mereferensikan path lama (dicek `grep`).
- `improvement-26-september-2026.md` tetap di root sesuai permintaan.

### 12. Tidak ada `.gitignore` — ✅

`.claude/settings.local.json` (berisi path lokal & permission luas) tidak
di-ignore.

**Perubahan.** `.gitignore` baru: `.claude/settings.local.json`, output build
(`/esb`, `/bin/`, `*.test`, `*.out`), `.DS_Store`, `.idea/`, `.vscode/`.
Sebelumnya tak ada file ter-track yang cocok dengan pola ini (dicek
`git ls-files`).

**Verifikasi.** `git check-ignore -v .claude/settings.local.json` →
`.gitignore:2`; `git status --ignored` hanya menampilkan `.claude/`.

### 13. CI kurang ketat — ✅

8 file belum gofmt; CI tidak cek gofmt, tidak ada `govulncheck`
(risk area: dependency vulnerability management), tidak `-race`.

**Temuan penting saat dikerjakan.** `govulncheck` untuk `esb` sendiri: bersih.
Tapi **proyek hasil generate** (dependency di-pin oleh `go.mod.tmpl`):
- **GO-2026-5970** — infinite loop di `golang.org/x/text@v0.14.0`, **reachable**
  (`wire.init → gorm.init → norm.Form.Properties`). Fixed di v0.39.0.
  Asal: `gorm.io/gorm v1.25.12`; `gorm v1.31.2` (terbaru) pun masih menarik
  x/text v0.20.0 → tetap rentan.
- **GO-2026-5024** — `golang.org/x/sys@v0.7.0` (khusus Windows, tidak
  dipanggil). Fixed di v0.44.0. Asal: `glebarez/sqlite` → modernc v1.23.1.

(ID vuln dan versi fixed berasal langsung dari output govulncheck v1.8.0.)

**Perbaikan.**
- `go.mod.tmpl`: `gorm.io/gorm v1.31.2`, `jwt/v5 v5.3.1` (samakan dengan esb),
  pin minimum `golang.org/x/text v0.39.0` + `golang.org/x/sys v0.44.0`
  (`// indirect`, bertahan setelah `go mod tidy`). Hasil scan: **No
  vulnerabilities found**.
- `gofmt -w` 7 file tersisa (1 file sudah ter-format di task 7).
- CI (`.github/workflows/ci.yml`): step `Check formatting` (`gofmt -l`),
  `go test -race ./...`, `govulncheck` untuk esb, dan **`govulncheck` untuk
  proyek hasil generate dengan semua recipe** (menangkap vuln dari
  `go.mod.tmpl`). Versi govulncheck di-pin `v1.8.0` via env.

**Catatan maintenance (belum dikerjakan).** `glebarez/sqlite v1.11.0` →
`glebarez/go-sqlite v1.21.2` masih mem-pin `modernc.org/sqlite v1.23.1` (2023).
Sudah dicoba: menaikkan ke `modernc.org/sqlite v1.53.0` compile dan semua test
(termasuk stress konkurensi) lulus, tapi itu kombinasi fork-driver lama +
library baru yang tidak didukung resmi → tidak diterapkan. Pantau lewat step
govulncheck CI.

**Verifikasi.**
- `go test -race ./...` hijau (~40 detik) — sekaligus memvalidasi lompatan
  gorm v1.25 → v1.31 pada semua template/recipe.
- Script step "generated project" dijalankan lokal: No vulnerabilities found.
- YAML valid (di-parse Ruby); `go mod tidy` tanpa diff; `go vet` bersih;
  `gofmt -l .` kosong.

### 14. Tidak ada tag rilis — ✅ *(pembuatan tag pertama ⏸️ keputusan user)*

`go install @latest` → pseudo-version.

**Perubahan.**
- `.github/workflows/release.yml` baru, jalan saat tag `v*.*.*` di-push:
  `go test`, build `CGO_ENABLED=0` untuk linux/amd64, linux/arm64,
  darwin/amd64, darwin/arm64, windows/amd64 dengan
  `-ldflags -X github.com/ariefsam/esb/cmd.version=$GITHUB_REF_NAME`,
  `checksums.txt` (sha256), lalu `gh release create --verify-tag --generate-notes`.
  Permission hanya `contents: write`; nama tag dipakai lewat env
  `$GITHUB_REF_NAME` (bukan interpolasi `${{ }}` di script).
- README: cara install versi tertentu + section **Rilis (maintainer)**.

**Tidak dikerjakan (butuh keputusan user).** Membuat & push tag pertama
(mis. `v0.1.0`) — memilih nomor versi dan push tag adalah aksi outward.

**Verifikasi.** YAML valid; build loop dijalankan lokal untuk kelima target →
semua sukses (±15 MB/binary); `esb version` pada binary hasil →
`esb v0.0.0-test`.

### 15. `make migrate-*` tidak memigrasi — ✅ *(temuan baru saat task 10)*

**Masalah** (terbukti empiris). Makefile hasil generate:
- `migrate-to-esb` menjalankan `go run . migrate to-esb ...` → itu **aplikasi
  hasil generate**, yang mengabaikan argumen → output
  `Starting shop on 127.0.0.1:19088`, server menyala, tidak ada migrasi.
- Membaca `$ESB_URL`/`$TENANT_ID`/... dari shell; `make` tidak memuat `.env`
  → flag kosong walau `.env` sudah diisi.
- `migrate-to-embedded` **selalu** `--force` → safety check CLI (tolak kalau
  SQLite target sudah berisi event) tidak pernah berlaku.
- Komentar "Setelah sukses, .env akan di-update ke EVENT_STORE_MODE=esb-server"
  salah: `esb migrate` hanya menulis `<dsn>.migration_state`.

**Perbaikan** (`makefile.tmpl`):
- Target memanggil CLI `$(ESB) migrate ...` (`ESB ?= esb`, bisa di-override).
- `-include .env` (tanpa `export`, jadi `make run` tidak terpengaruh);
  `--source` = `EVENT_STORE_DSN` → `DB_DSN` → `app.db`.
- `--force` hanya dengan `make migrate-to-embedded FORCE=1`.
- Komentar diperbaiki: ubah `EVENT_STORE_MODE` manual setelah migrasi.

**Verifikasi.** Test baru `generator/makefile_migrate_test.go` (`make -n`,
skip kalau `make` tak ada): argv tepat untuk ketiga varian. Template lama:
**FAIL** (menampilkan `go run . migrate ...` + `--force` selalu).

### 16. Handler `esb add handler` tidak bisa di-route — ⏸️ *(butuh keputusan)*

**Masalah** (terbukti). `add handler` menyisipkan hint
`// TODO: router.HandleFunc("/place-order", app.PlaceOrderHandler.Handle)...`
ke `server/routes.go`, tapi `RegisterRoutes(router *mux.Router)` tidak punya
akses ke `app`. Di-uncomment → `server/routes.go:15:36: undefined: app`.
Handler sudah dikonstruksi di `wire.NewApp()` tetapi tidak pernah terjangkau
lewat HTTP tanpa rewiring manual. README sudah diberi catatan sementara.

**Usulan (belum dikerjakan — mengubah desain proyek hasil generate).**
Tambah marker `// esb:inject:app-routes` di `wire/wire.go` setelah
`server.RegisterRoutes(router)`, lalu `add handler` menyisipkan route **aktif**
`router.HandleFunc("/place-order", placeOrderHandler.Handle).Methods(http.MethodPost)`.
Proyek lama tanpa marker → fallback ke hint di `routes.go` (tetap compile).
Keputusan yang dibutuhkan: route langsung aktif (endpoint skeleton ter-expose)
atau tetap berupa hint yang diperbaiki agar bisa di-uncomment.

