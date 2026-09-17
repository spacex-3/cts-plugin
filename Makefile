PLUGIN := codex-turn-state

.PHONY: fmt test build package
fmt:
	gofmt -w .
test:
	go test -race ./...
	go vet ./...
build:
	mkdir -p dist
	CGO_ENABLED=1 go build -trimpath -buildmode=c-shared -o dist/$(PLUGIN).so .
package: build
	python3 scripts/package.py
