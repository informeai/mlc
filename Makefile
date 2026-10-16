BIN := bin/mlc

.PHONY: build test check fmt clean

# make (ou make build): gera o compilador em bin/mlc
build:
	go build -o $(BIN) ./cmd/mlc

# make test: roda a suíte (programas, panics e erros de compilação)
test:
	go test ./...

# make check: o que precisa estar limpo antes de considerar uma mudança pronta
# (os .mlc de tests/errors ficam de fora: vários têm erro de sintaxe de propósito)
MLC_SRC := std tests/run tests/panic

check: build
	@test -z "$$(gofmt -l .)" || { echo "arquivos sem gofmt:"; gofmt -l .; exit 1; }
	@test -z "$$($(BIN) fmt -l $(MLC_SRC))" || { echo "arquivos sem mlc fmt:"; $(BIN) fmt -l $(MLC_SRC); exit 1; }
	go vet ./...
	go test ./...

# make fmt: formata o Go (gofmt) e o MLC (mlc fmt)
fmt: build
	gofmt -w .
	$(BIN) fmt $(MLC_SRC)

clean:
	rm -rf bin
