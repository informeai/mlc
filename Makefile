BIN := bin/mlc

.PHONY: build test test-std check fmt clean

# make (ou make build): gera o compilador em bin/mlc
build:
	go build -o $(BIN) ./cmd/mlc

# make test: roda a suíte (programas, panics, erros de compilação e os
# testes da biblioteca padrão)
test:
	go test ./...

# make test-std: roda os testes da biblioteca padrão com o mlc test
test-std: build
	$(BIN) test std

# make check: o que precisa estar limpo antes de considerar uma mudança pronta
# (os .mlc de tests/errors ficam de fora: vários têm erro de sintaxe de propósito)
MLC_SRC := std tests/run tests/panic tests/testing

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
