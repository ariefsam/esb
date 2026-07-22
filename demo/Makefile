.PHONY: run build test keygen

run:
	go run .

build:
	go build -o bin/demo .

test:
	go test ./...

keygen:
	@openssl ecparam -name prime256v1 -genkey -noout -out private.pem
	@openssl ec -in private.pem -pubout -out public.pem
	@printf '\nPUBLIC_KEY='
	@base64 -b 0 public.pem 2>/dev/null || base64 -w 0 public.pem
	@printf '\n\nPaste PUBLIC_KEY into the ESB server PUBLIC_KEYS env var.\n'
