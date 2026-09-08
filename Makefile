.PHONY: build test test-proto-contract test-proto-compatibility test-python-sdk test-python-integrations test-typescript-sdk test-java-sdk test-rust-sdk test-dotnet-sdk test-dashboard race benchmark vet fmt run docker validate-single-node validate-kubernetes

DOTNET ?= dotnet

build:
	go build -trimpath -ldflags "-X main.version=$${VERSION:-dev} -X main.commit=$${COMMIT:-unknown} -X main.buildDate=$${BUILD_DATE:-unknown}" ./cmd/gideondb

docker:
	docker build --build-arg VERSION="$${VERSION:-dev}" --build-arg COMMIT="$${COMMIT:-unknown}" --build-arg BUILD_DATE="$${BUILD_DATE:-unknown}" -t "$${IMAGE:-gideondb:dev}" .

validate-single-node:
	go run ./cmd/gideondb-loadtest -vectors "$${VECTORS:-100000}" -dimension "$${DIMENSION:-128}" -shards "$${SHARDS:-8}" -queries "$${QUERIES:-200}" -concurrency "$${CONCURRENCY:-8}" -wal-sync "$${WAL_SYNC:-always}"

validate-kubernetes:
	./scripts/kubernetes-gate.sh

test:
	go test ./...
	$(MAKE) test-proto-contract
	$(MAKE) test-python-sdk
	$(MAKE) test-python-integrations
	$(MAKE) test-typescript-sdk
	$(MAKE) test-java-sdk
	$(MAKE) test-rust-sdk
	$(MAKE) test-dotnet-sdk
	$(MAKE) test-dashboard

test-proto-contract:
	python3 scripts/check-proto-contract.py
	$(MAKE) test-proto-compatibility

test-proto-compatibility:
	go test ./internal/api/grpcapi -run '^TestV1WireCompatibility$$'

test-python-sdk:
	PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=sdk/python/src python3 -m unittest discover -s sdk/python/tests -v

test-python-integrations:
	PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=sdk/python/src:integrations/python python3 -m unittest discover -s integrations/python/tests -v
	PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=sdk/python/src:integrations/python python3 -m py_compile examples/ollama_semantic_search.py examples/huggingface_semantic_search.py examples/openai_compatible_semantic_search.py examples/cohere_semantic_search.py

test-typescript-sdk:
	npm test --prefix sdk/typescript

test-java-sdk:
	cd sdk/java && sh test.sh

test-rust-sdk:
	cargo test --manifest-path sdk/rust/Cargo.toml --locked

test-dotnet-sdk:
	DOTNET_CLI_TELEMETRY_OPTOUT=1 $(DOTNET) run --project sdk/dotnet/tests/GideonDB.Client.Tests/GideonDB.Client.Tests.csproj --configuration Release

test-dashboard:
	node --check internal/api/rest/dashboard/app.js

race:
	go test -race ./...

benchmark:
	go test -run '^$$' -bench . -benchmem ./internal/distance ./internal/index/flat ./internal/index/hnsw ./internal/metadata ./internal/wal

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal pkg

run:
	go run ./cmd/gideondb -data-path ./data -http-address 127.0.0.1:6333
