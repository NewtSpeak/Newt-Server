.PHONY: swagger test frontend build run dev

swagger:
	swag init -g cmd/server/main.go -o docs --parseInternal

test:
	go test ./...

frontend:
	bun --cwd=../frontend run build
	rm -rf internal/web/dist/assets internal/web/dist/index.html internal/web/dist/favicon.ico
	cp -R ../frontend/build/client/. internal/web/dist/

build: swagger frontend
	go build -o bin/newt-server ./cmd/server

run:
	go run ./cmd/server

dev:
	@bun --cwd=../frontend run dev --host 127.0.0.1 & frontend_pid=$$!; \
	trap 'kill $$frontend_pid 2>/dev/null || true' EXIT INT TERM; \
	cd .. && air
