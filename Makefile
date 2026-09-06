.DEFAULT_GOAL := check
.PHONY: check proto-lint proto-gen proto-verify go-test go-build rust-test rust-build tools

# Everything CI runs.
check: proto-lint go-test rust-test

# --- Contracts -------------------------------------------------------------

proto-lint:
	buf lint
	buf build -o /dev/null

proto-gen:
	buf generate

# Fails if the committed bindings differ from what the contracts generate.
proto-verify: proto-gen
	git diff --exit-code -- protocol/gen

# --- Go --------------------------------------------------------------------

go-build:
	go build ./...

go-test:
	go vet ./...
	go test ./... -race

# --- Rust ------------------------------------------------------------------

rust-build:
	cargo build --workspace

rust-test:
	cargo fmt --all -- --check
	cargo clippy --workspace --all-targets -- -D warnings
	cargo test --workspace

# --- Tooling ---------------------------------------------------------------

tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
