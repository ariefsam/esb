# Panduan Flow: `esb ui`, `esb show flow`, `esb doctor`

Panduan ini untuk developer proyek yang dibuat dengan `esb`. Isinya: cara
melihat alur kode proyek, cara membaca graph-nya, dan apa yang harus dilakukan
kalau graph-nya salah atau kurang lengkap.

Semua yang ada di Flow **dibaca dari source code** proyek (AST Go), bukan dari
event yang tersimpan. Tidak ada yang ditulis ke proyek atau ke event store.

## Tiga cara melihat alur yang sama

| Cara | Untuk |
|---|---|
| `esb ui`, lalu buka `/flow` | menjelajah graph secara interaktif |
| `esb show flow [aggregate] [-o yaml\|json]` | data graph untuk script, review, atau dokumentasi |
| `esb doctor [--strict] [-o json]` | daftar apa yang tidak dipahami esb dan apa yang belum ada, untuk diperbaiki atau dipasang di CI |

Ketiganya memakai hasil scan yang sama, jadi angkanya selalu cocok.

## Alur kerja yang disarankan

1. **Jalankan `esb doctor`** di root proyek.
2. **Baca bagian "Tidak dipahami scanner".** Ini bagian kode yang tidak bisa
   dibaca esb, jadi graph bisa kurang lengkap. Setiap temuan punya `file:line`
   dan perbaikannya, biasanya sebuah [anotasi](#anotasi).
3. **Baca bagian "Celah alur".** Ini alur yang memang belum ada, misalnya event
   yang tidak pernah di-emit atau tidak di-handle projection mana pun. Perbaiki
   kodenya, atau tandai dengan anotasi kalau memang disengaja.
4. **Cek hasilnya di `/flow`.** Klik node yang ingin diperiksa untuk menyorot
   jalurnya.
5. **Pasang di CI:** `esb doctor --strict` gagal (exit 1) kalau ada temuan
   berlevel `warn`, termasuk celah alur.

## Membaca graph di `/flow`

### Kolom

```
HTTP handler → Service method → Event → Event store → Projection → Query
```

| Kolom | Isinya |
|---|---|
| HTTP handler | method exported pada struct `<X>Handler` di `server/handler/` |
| Service method | command (method yang menyimpan event) dan method lain yang dipanggil handler dan menyentuh event store atau read model |
| Event | event yang dideklarasikan di `domain/` |
| Event store | satu node per aggregate stream |
| Projection | projection worker yang dijalankan di `main.go` |
| Query | fungsi di `projection/` yang membaca read model, atau yang menulisnya (bertanda "tulis read model") |

### Pita (swimlane)

Setiap aggregate menjadi satu pita horizontal di semua kolom, jadi satu alur
umumnya terbaca lurus dari kiri ke kanan. Node yang tidak milik satu aggregate
tertentu, misalnya resolver atau projection multi-aggregate seperti
`read_model`, ada di pita **"lintas aggregate"** paling atas.

### Garis

| Garis | Arti |
|---|---|
| abu solid | panggilan (handler → method), emit (method → event), event di-handle projection, projection mengisi tabel yang dibaca query |
| oranye | tulis ke event store: event masuk ke stream aggregate-nya, atau method menulis aggregate lain lewat command service lain |
| biru putus-putus | baca dari event store: method memuat aggregate tanpa menulisnya (baca lintas aggregate, atau handler yang membaca write model langsung) |
| hijau putus-putus | baca read model: handler atau service memanggil `projection.F(...)` langsung |
| merah | tulis read model: handler atau service menulis read model langsung, di luar projection worker |
| abu putus-putus | tebakan dari nama (projection → query), hanya dipakai kalau tabel query tidak terbaca |

Command selalu memuat aggregate-nya sendiri sebelum menyimpan. Baca itu sengaja
tidak digambar karena hampir setiap command akan mendapat garis tambahan.

### Label dan peringatan

Kotak bergaris oranye menandai jalur buntu. Teks peringatannya:

| Peringatan | Arti | Biasanya |
|---|---|---|
| `no command emits it` | tidak ada command yang menyimpan event ini | belum diimplementasi, atau nama event dihitung saat runtime → `// esb:emits` |
| `no projection handles it` | tidak ada projection yang meng-handle event ini | belum ada projection, atau memang sengaja → `// esb:no-projection` |
| `no producer, no consumer` | gabungan dua di atas | event belum dipakai |
| `no projection feeds it` | tidak ada projection yang menulis tabel yang dibaca query ini | tabel belum diisi projection mana pun |
| `subscribes but handles no event yet` | worker subscribe ke aggregate tapi belum punya `case` | worker hasil generate yang belum diisi |
| `event name computed at runtime` | command memanggil `store()` dengan nama event non-literal | tambahkan `// esb:emits` |
| `emitted but not declared in domain/` | command menyimpan event yang struct-nya tidak ada di `domain/` | salah ketik nama event, atau struct-nya terhapus |
| `calls no known command` | handler memanggil sesuatu yang tidak menyentuh event store maupun read model | logout/health → `// esb:ignore` |
| `no service call yet` | handler masih body TODO hasil generate | belum diimplementasi |

Subjudul **"baca write model langsung"** pada handler berarti handler itu hanya
memuat aggregate dari event store, bukan dari read model. Ini tidak selalu
salah, tapi perlu disadari.

### Interaksi

- **Klik node** atau **cari namanya** (tekan `/`) untuk menyorot jalurnya dari
  handler sampai query. Node yang dipilih tersimpan di `?focus=...`, jadi link-nya
  bisa dibagikan.
- **Lihat kode**: tombol di bar detail, atau klik dua kali pada node. Kode tampil
  read-only.
- **Filter** langsung berlaku tanpa reload:
  - beberapa aggregate sekaligus (`?aggregate=a&aggregate=b`)
  - "Hanya masalah" (`?problems=1`): node berperingatan beserta tetangganya
  - "Sembunyikan handler belum jadi" (`?stubs=hide`)
- **Legenda** sekaligus menjadi toggle untuk menyembunyikan jenis garis tertentu.
- **Zoom**: tombol −, Fit, dan +. Judul kolom tetap terlihat saat scroll.
- Baris di **Gaps** dan lokasi di **Tidak dipahami scanner** bisa diklik untuk
  langsung menuju node atau kodenya.

## Agar kode terbaca esb

esb mengenali pola yang dihasilkan `esb add` beserta variasi umumnya. Kode di luar
pola ini tetap jalan, hanya tidak tergambar. `esb doctor` akan memberi tahu bagian
mana, dan anotasi bisa menutup celahnya.

| Lapisan | Yang dikenali |
|---|---|
| Aggregate & event | `domain/<aggregate>.go`: konstanta `<X>AggregateName`, struct event, dan `case "Event"` di `Apply()` |
| Command | method exported di `service/` yang memanggil `s.store(ctx, agg, "Event", …)` atau `s.storeWithKey(…)` dengan nama event literal, langsung atau lewat helper unexported, termasuk helper service lain yang dipanggil lewat field (`s.cycles.attachEnvelope`) |
| Akses event store | pemanggilan method pada field bertipe `…EventRepository`: `Store*` = tulis, lainnya = baca |
| Handler | struct `<X>Handler` di `server/handler/` dengan field `*service.<T>` (nama field bebas) dan pemanggilan `projection.F(...)` |
| Projection | file `projection/<nama>_worker.go` yang worker-nya **didaftarkan** di blok `// esb:inject:projection-workers` di `main.go`. Worker standalone yang tidak didaftarkan dilebur ke worker multi-aggregate yang mencakup aggregate-nya. |
| Event yang di-handle | `case "Event":` di dalam `switch e.EventName` |
| Tabel read model | tipe `<X>Row` di `projection/` yang punya `TableName()` atau terdaftar di `AutoMigrate` |
| Query | fungsi exported `func F(ctx context.Context, db *gorm.DB, …)` di `projection/`. Tabelnya diambil dari tipe row yang disebut, atau dari nama tabel di SQL mentah. Fungsi yang memanggil `Create`, `Save`, `Update`, `Delete`, atau `Exec` dihitung sebagai penulis read model. |

## Anotasi

Anotasi adalah komentar `esb:` di kode proyek. Anotasi mengalahkan tebakan
scanner. Taruh di komentar dokumentasi fungsi atau tipe, kecuali
`esb:no-projection` yang juga boleh di mana saja di file domain.

```go
// Transition stores the event the state machine picks.
// esb:emits OrderPlaced, OrderShipped, cart/CartClosed
func (s *OrderService) Transition(ctx context.Context, cmd TransitionCmd) error {
	return s.store(ctx, agg, next.Event, data)
}
```

| Anotasi | Di mana | Efek |
|---|---|---|
| `esb:emits A, B, agg/C` | method service | event yang disimpan method ini (`agg/Event` untuk aggregate lain). Peringatan "computed at runtime" hilang. |
| `esb:reads agg` | method service | method ini membaca aggregate tersebut dari event store |
| `esb:writes agg` | method service | method ini menulis aggregate tersebut ke event store |
| `esb:no-projection` | file domain | aggregate ini sengaja tanpa projection |
| `esb:no-projection` | doc tipe event | event itu saja yang sengaja tanpa projection |
| `esb:ignore` | handler, service, atau fungsi projection | keluarkan dari flow |

Contoh aggregate yang menjawab query dari write model:

```go
// domain/user_settings.go

// UserSettings is answered from the write model; it has no read model.
// esb:no-projection
const UserSettingsAggregateName = "user-settings"
```

## Kode temuan `esb doctor`

| Kode | Level | Arti | Perbaikan |
|---|---|---|---|
| `parse-error` | warn | file Go tidak bisa di-parse, jadi dilewati | perbaiki syntax-nya |
| `dynamic-event` | warn | `store()` dipanggil dengan nama event non-literal | `// esb:emits` di atas method |
| `handler-unknown-call` | info | handler memanggil method yang tidak menyentuh event store maupun read model | `// esb:ignore` di handler, atau `// esb:reads` / `// esb:writes` di method service |
| `query-no-table` | info | fungsi di `projection/` tanpa tabel yang dikenali, jadi hubungannya ke projection hanya ditebak | pakai tipe `<X>Row` dengan `TableName()`, atau sebut nama tabelnya di SQL |

Exit code: `1` kalau ada temuan "tidak dipahami" berlevel `warn`. Dengan
`--strict`, celah alur berlevel `warn` juga dihitung.

## `esb show flow` untuk script

```bash
esb show flow -o json | jq '[.nodes[] | select(.warn != "")]'           # semua jalur buntu
esb show flow transaction -o json | jq '.edges[] | select(.op == "read")'  # baca event store lintas aggregate
```

Isi export:

- `version`, `project`, `filter`, `stats`
- `nodes[]`: `id`, `kind`, `label`, `sub`, `aggregate`, `warn`, `file`, `line`
- `edges[]`: `from`, `to`, `inferred`, dan `op` untuk edge yang punya op (`read`, `write`, `rm-read`, `rm-write`)
- `gaps[]`

Endpoint `/flow.json[?aggregate=…]` di `esb ui` memberi data yang sama.

## Batasan

- Hanya package `service`, `server/handler`, `projection`, dan `domain` yang
  dibaca. Pemanggilan ke package lain tidak diikuti.
- Nama tabel di SQL dicocokkan sebagai kata utuh di string literal mana pun.
  String biasa yang kebetulan berisi kata yang sama dengan nama tabel ikut
  terhitung.
- Method service yang bukan command dan tidak dipanggil handler tidak tampil
  sebagai node.
- Hasil scan mengikuti isi file saat itu. Refresh `/flow` setelah mengubah kode.
