BIN := bin/mlc

.PHONY: build test check fmt clean

# make (ou make build): gera o compilador em bin/mlc
build:
	go build -o $(BIN) ./cmd/mlc

# make test: roda a suíte (programas, panics e erros de compilação)
test:
	go test ./...

# make check: o que precisa estar limpo antes de considerar uma mudança pronta
check:
	@test -z "$$(gofmt -l .)" || { echo "arquivos sem gofmt:"; gofmt -l .; exit 1; }
	go vet ./...
	go test ./...

fmt:
	gofmt -w .

clean:
	rm -rf bin
