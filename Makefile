# Knotwork host-side CLI: `knotwork` (serve, admin, auth, hook).
MISE := mise

.PHONY: all build web test lint fmt vet run clean check

all: check build

build: web
	$(MISE) exec -- go build -o bin/knotwork ./cmd/knotwork

web:
	cd frontend && $(MISE) exec -- pnpm build

test:
	$(MISE) exec -- go test ./...

lint:
	$(MISE) exec -- golangci-lint run ./...

fmt:
	$(MISE) exec -- gofmt -w cmd internal

vet:
	$(MISE) exec -- go vet ./...

check: fmt vet test

run:
	$(MISE) exec -- go run ./cmd/knotwork serve

clean:
	rm -rf bin
