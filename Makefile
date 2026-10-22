BIN := bin/mlc

.PHONY: build test test-std check sanitize mutate fuzz enum robust cover fmt clean

# make (or make build): builds the compiler into bin/mlc
build:
	go build -o $(BIN) ./cmd/mlc

# make test: runs the suite (programs, panics, compile errors and the
# standard library's tests)
test:
	go test ./...

# make test-std: runs the standard library's tests with mlc test
test-std: build
	$(BIN) test std

# make check: what must be clean before considering a change done
# (the .mlc files of tests/errors are left out: several have syntax errors on purpose)
MLC_SRC := std tests/run tests/panic tests/testing

check: build
	@test -z "$$(gofmt -l .)" || { echo "files not gofmt-ed:"; gofmt -l .; exit 1; }
	@test -z "$$($(BIN) fmt -l $(MLC_SRC))" || { echo "files not mlc fmt-ed:"; $(BIN) fmt -l $(MLC_SRC); exit 1; }
	go vet ./...
	go test ./...

# make sanitize: runs the test programs (tests/run, tests/panic, tests/ffi
# and the std's tests) under AddressSanitizer + UndefinedBehaviorSanitizer and
# then under ThreadSanitizer. Every program the checker accepts must run
# clean (see docs/verification.md).
SANITIZE_TESTS := 'TestRun|TestPanic|TestStdLibrary|TestFFIOutHandle|TestFFICallbacks|TestFFICStructs'

sanitize:
	MLC_SANITIZE=address go test ./internal/driver -run $(SANITIZE_TESTS) -count=1
	MLC_SANITIZE=thread go test ./internal/driver -run $(SANITIZE_TESTS) -count=1

# make mutate: applies mutations that create ownership bugs (deleting or repeating a
# free, freeing before the use, moving twice...) to the test programs. The
# checker must refuse each mutant; an accepted mutant runs under
# AddressSanitizer and the allocation counter, and an error there is a hole in the
# checker (the mutant is kept in MUTATE_OUT). See docs/verification.md.
MUTATE_OUT ?= /tmp/mlc-mutate

mutate:
	@MLC_MUTATE=1 MLC_MUTATE_OUT=$(MUTATE_OUT) go test ./internal/fuzz -run 'TestMutate$$' -count=1 -timeout 90m > /dev/null; status=$$?; cat $(MUTATE_OUT)/report.txt; exit $$status

# make fuzz: generates random programs focused on ownership for FUZZTIME (default
# 1m), half valid and half with an injected bug that execution sees. A bug
# accepted by the checker is a hole: the program is reduced to the smallest one that
# still reproduces it and kept in FUZZ_OUT. The refused valid ones show up grouped,
# as false-positive candidates. See docs/verification.md.
FUZZTIME ?= 1m
FUZZ_OUT ?= /tmp/mlc-fuzz

fuzz:
	@MLC_FUZZ=1 FUZZTIME=$(FUZZTIME) MLC_FUZZ_OUT=$(FUZZ_OUT) go test ./internal/fuzz -run 'TestFuzz$$' -count=1 -timeout 0 > /dev/null; status=$$?; cat $(FUZZ_OUT)/report.txt; exit $$status

# make enum: every program up to size ENUM (default 5, about 30 s; 6
# takes about 10 min) over a small alphabet of ownership statements, each one
# judged by the checker and by a reference model that follows every
# path. The checker accepting a program the model says is unsafe is a
# hole; refusing a safe one is a false positive, cataloged by message. See
# docs/verification.md.
ENUM ?= 5
ENUM_OUT ?= /tmp/mlc-enum

enum:
	@MLC_ENUM=$(ENUM) MLC_ENUM_OUT=$(ENUM_OUT) go test ./internal/fuzz -run 'TestEnumerate$$' -count=1 -timeout 0 > /dev/null; status=$$?; cat $(ENUM_OUT)/report.txt; exit $$status

# make robust: go test -fuzz over the compiler (parser, checker and C
# generator) and the formatter, for ROBUSTTIME each (default 1m): the compiler never
# panics, and formatting twice gives the same text. An input that fails
# is kept in internal/driver/testdata/fuzz and runs on every go test.
ROBUSTTIME ?= 1m

robust:
	go test ./internal/driver -run '^$$' -fuzz '^FuzzCheck$$' -fuzztime $(ROBUSTTIME)
	go test ./internal/driver -run '^$$' -fuzz '^FuzzFormat$$' -fuzztime $(ROBUSTTIME)

# make cover: the checker's coverage by the suite, mutation, fuzzing and
# enumeration, with the list of checker errors no test triggers.
COVER_OUT ?= /tmp/mlc-cover

cover:
	@mkdir -p $(COVER_OUT)
	@go test ./internal/driver ./internal/fuzz ./internal/lsp -coverpkg=./internal/checker -coverprofile=$(COVER_OUT)/checker.out -count=1 > /dev/null
	@go tool cover -func=$(COVER_OUT)/checker.out | tail -1
	@go tool cover -html=$(COVER_OUT)/checker.out -o $(COVER_OUT)/checker.html
	@MLC_COVER=$(COVER_OUT)/checker.out go test ./internal/fuzz -run 'TestCoverReport$$' -count=1 > /dev/null; cat $(COVER_OUT)/report.txt
	@echo "page with the lines: $(COVER_OUT)/checker.html"

# make fmt: formats the Go (gofmt) and the MLC (mlc fmt)
fmt: build
	gofmt -w .
	$(BIN) fmt $(MLC_SRC)

clean:
	rm -rf bin
