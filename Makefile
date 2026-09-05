.PHONY: build test test-python-sdk test-typescript-sdk test-java-sdk test-dashboard race benchmark vet fmt run docker validate-single-node validate-kubernetes

build:
	go build -trimpath -ldflags "-X main.version=$${VERSION:-dev} -X main.commit=$${COMMIT:-unknown} -X main.buildDate=$${BUILD_DATE:-unknown}" ./cmd/vectordb

docker:
	docker build --build-arg VERSION="$${VERSION:-dev}" --build-arg COMMIT="$${COMMIT:-unknown}" --build-arg BUILD_DATE="$${BUILD_DATE:-unknown}" -t "$${IMAGE:-vectordb:dev}" .

validate-single-node:
	go run ./cmd/vectordb-loadtest -vectors "$${VECTORS:-100000}" -dimension "$${DIMENSION:-128}" -shards "$${SHARDS:-8}" -queries "$${QUERIES:-200}" -concurrency "$${CONCURRENCY:-8}" -wal-sync "$${WAL_SYNC:-always}"

validate-kubernetes:
	./scripts/kubernetes-gate.sh

test:
	go test ./...
	$(MAKE) test-python-sdk
	$(MAKE) test-typescript-sdk
	$(MAKE) test-java-sdk
	$(MAKE) test-dashboard

test-python-sdk:
	PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=sdk/python/src python3 -m unittest discover -s sdk/python/tests -v

test-typescript-sdk:
	npm test --prefix sdk/typescript

test-java-sdk:
	cd sdk/java && sh test.sh

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
	go run ./cmd/vectordb -data-path ./data -http-address 127.0.0.1:6333
