.PHONY: run run-embedded build test keygen migrate-to-esb migrate-to-embedded

run:
	go run .

# Default run pakai mode embedded (lihat .env.example).
# Alias dari `run`, dipertahankan supaya orang yang terbiasa ngetik
# `make run-embedded` tidak bingung saat EVENT_STORE_MODE di .env
# mereka bukan embedded.
run-embedded:
	go run .

build:
	go build -o bin/mydemo .

test:
	go test ./...

keygen:
	@openssl ecparam -name prime256v1 -genkey -noout -out private.pem
	@openssl ec -in private.pem -pubout -out public.pem
	@printf '\nPUBLIC_KEY='
	@base64 -b 0 public.pem 2>/dev/null || base64 -w 0 public.pem
	@printf '\n\nPaste PUBLIC_KEY into the ESB server PUBLIC_KEYS env var.\n'

# Migrasi event dari SQLite lokal ke server ESB remote.
# Setelah sukses, .env akan di-update ke EVENT_STORE_MODE=esb-server
# dan worker harus di-restart manual.
migrate-to-esb:
	go run . migrate to-esb --source "$${EVENT_STORE_DSN:-$${DB_DSN:-app.db}}" --esb-url "$$ESB_URL" --tenant "$$TENANT_ID" --project "$$PROJECT_ID"

# Migrasi event dari server ESB remote kembali ke SQLite lokal.
# Butuh --force saat ada data existing di SQLite target.
migrate-to-embedded:
	go run . migrate to-embedded --source "$${EVENT_STORE_DSN:-$${DB_DSN:-app.db}}" --esb-url "$$ESB_URL" --tenant "$$TENANT_ID" --project "$$PROJECT_ID" --force