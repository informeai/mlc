BIN := bin/mlc

.PHONY: build test test-std check sanitize mutate fuzz enum robust cover fmt clean

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

# make enum: todos os programas até o tamanho ENUM (padrão 5, uns 30 s; 6
# leva uns 10 min) sobre um alfabeto pequeno de comandos de posse, cada um
# julgado pelo verificador e por um modelo de referência que segue todos os
# caminhos. O verificador aceitar um programa que o modelo diz inseguro é
# buraco; recusar um seguro é falso positivo, catalogado pela mensagem. Veja
# docs/verificacao.md.
ENUM ?= 5
ENUM_OUT ?= /tmp/mlc-enum

enum:
	@MLC_ENUM=$(ENUM) MLC_ENUM_OUT=$(ENUM_OUT) go test ./internal/fuzz -run 'TestEnumerate$$' -count=1 -timeout 0 > /dev/null; status=$$?; cat $(ENUM_OUT)/relatorio.txt; exit $$status

# make robust: go test -fuzz sobre o compilador (parser, checker e gerador
# de C) e o formatador, por ROBUSTTIME cada (padrão 1m): o compilador nunca
# dá panic, e formatar duas vezes dá o mesmo texto. Uma entrada que falha
# fica em internal/driver/testdata/fuzz e passa a rodar a cada go test.
ROBUSTTIME ?= 1m

robust:
	go test ./internal/driver -run '^$$' -fuzz '^FuzzCheck$$' -fuzztime $(ROBUSTTIME)
	go test ./internal/driver -run '^$$' -fuzz '^FuzzFormat$$' -fuzztime $(ROBUSTTIME)

# make cover: a cobertura do checker pela suíte, a mutação, o fuzz e a
# enumeração, com a lista dos erros do verificador que nenhum teste dispara.
COVER_OUT ?= /tmp/mlc-cover

cover:
	@mkdir -p $(COVER_OUT)
	@go test ./internal/driver ./internal/fuzz ./internal/lsp -coverpkg=./internal/checker -coverprofile=$(COVER_OUT)/checker.out -count=1 > /dev/null
	@go tool cover -func=$(COVER_OUT)/checker.out | tail -1
	@go tool cover -html=$(COVER_OUT)/checker.out -o $(COVER_OUT)/checker.html
	@MLC_COVER=$(COVER_OUT)/checker.out go test ./internal/fuzz -run 'TestCoverReport$$' -count=1 > /dev/null; cat $(COVER_OUT)/relatorio.txt
	@echo "página com as linhas: $(COVER_OUT)/checker.html"

# make fmt: formata o Go (gofmt) e o MLC (mlc fmt)
fmt: build
	gofmt -w .
	$(BIN) fmt $(MLC_SRC)

clean:
	rm -rf bin
