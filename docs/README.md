# docs

Catatan desain dan perencanaan `esb`. Dokumentasi pemakaian ada di
[README.md](../README.md) dan panduan di bawah.

## Panduan pemakaian

| Dokumen | Isi |
|---|---|
| [flow-guide.md](flow-guide.md) | `esb ui` `/flow`, `esb show flow`, `esb doctor`: alur kerja, cara membaca graph, konvensi agar kode terbaca, anotasi `esb:` |

## ideas/ — masih relevan

| Dokumen | Isi | Status per 2026-09-26 |
|---|---|---|
| [code-generation-idea.md](ideas/code-generation-idea.md) | Katalog blueprint/recipe dan fitur lintas-pola | Sebagian sudah jadi: recipe `crud`, `ledger`, `statemachine`, `saga`, `outbox`, `add upcaster`, `add idempotency`. Belum: `inventory`, `tally`. |

## archive/ — sudah diimplementasikan, disimpan sebagai catatan desain

| Dokumen | Isi |
|---|---|
| [2026-07-10-initial-plan.md](archive/2026-07-10-initial-plan.md) | Rencana awal CLI dan pola proyek yang di-generate |
| [2026-07-24-esb-ui-web-based-ui.md](archive/2026-07-24-esb-ui-web-based-ui.md) | Rencana `esb ui` (web UI lokal) |
| [2026-08-15-plan-ux-aggregate-page.md](archive/2026-08-15-plan-ux-aggregate-page.md) | Tambah/hapus event dari halaman aggregate (`esb delete event`) |
| [2026-09-26-improvement.md](archive/2026-09-26-improvement.md) | Hasil review 26 Sep 2026: 16 temuan, perbaikan, dan verifikasinya |

Referensi file/baris di dokumen arsip mengacu ke isi repo saat dokumen itu
ditulis dan mungkin sudah tidak cocok dengan kode sekarang.
