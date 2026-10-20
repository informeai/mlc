BIN := bin/mlc

.PHONY: build test test-std check sanitize mutate fuzz fmt clean

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

# make sanitize: roda os programas de teste (tests/run, tests/panic, tests/ffi
# e os testes da std) sob AddressSanitizer + UndefinedBehaviorSanitizer e
# depois sob ThreadSanitizer. Todo programa que o checker aceita precisa rodar
# limpo (veja docs/verificacao.md).
SANITIZE_TESTS := 'TestRun|TestPanic|TestStdLibrary|TestFFIOutHandle'

sanitize:
	MLC_SANITIZE=address go test ./internal/driver -run $(SANITIZE_TESTS) -count=1
	MLC_SANITIZE=thread go test ./internal/driver -run $(SANITIZE_TESTS) -count=1

# make mutate: aplica mutações que criam bugs de posse (apagar ou repetir um
# free, liberar antes do uso, mover duas vezes...) aos programas de teste. O
# verificador precisa recusar cada mutante; um mutante aceito roda sob
# AddressSanitizer e o contador de alocações, e um erro ali é um buraco no
# verificador (o mutante fica em MUTATE_OUT). Veja docs/verificacao.md.
MUTATE_OUT ?= /tmp/mlc-mutate

mutate:
	@MLC_MUTATE=1 MLC_MUTATE_OUT=$(MUTATE_OUT) go test ./internal/fuzz -run 'TestMutate$$' -count=1 -timeout 90m > /dev/null; status=$$?; cat $(MUTATE_OUT)/relatorio.txt; exit $$status

# make fuzz: gera programas aleatórios focados em posse por FUZZTIME (padrão
# 1m), metade válidos e metade com um bug injetado que a execução vê. Um bug
# aceito pelo verificador é um buraco: o programa é reduzido ao menor que
# ainda reproduz e fica em FUZZ_OUT. Os válidos recusados aparecem agrupados,
# como candidatos a falso positivo. Veja docs/verificacao.md.
FUZZTIME ?= 1m
FUZZ_OUT ?= /tmp/mlc-fuzz

fuzz:
	@MLC_FUZZ=1 FUZZTIME=$(FUZZTIME) MLC_FUZZ_OUT=$(FUZZ_OUT) go test ./internal/fuzz -run 'TestFuzz$$' -count=1 -timeout 0 > /dev/null; status=$$?; cat $(FUZZ_OUT)/relatorio.txt; exit $$status

# make fmt: formata o Go (gofmt) e o MLC (mlc fmt)
fmt: build
	gofmt -w .
	$(BIN) fmt $(MLC_SRC)

clean:
	rm -rf bin
