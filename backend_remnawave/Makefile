.PHONY: dev build templ css migrate fmt lint clean tools deps

GO ?= go
TEMPL_VERSION ?= v0.3.1001

tools:
	@command -v templ >/dev/null || $(GO) install github.com/a-h/templ/cmd/templ@$(TEMPL_VERSION)
	@command -v air   >/dev/null || $(GO) install github.com/air-verse/air@latest

deps: tools
	$(GO) mod download
	npm install --silent

dev: tools
	@echo ">> templ generate (watch), tailwind (watch), air (live reload)"
	@(npx tailwindcss -i ./web/styles/input.css -o ./web/static/dist.css --watch &) ; \
	(templ generate -watch &) ; \
	air -c .air.toml || $(GO) run ./cmd/server

build: templ css
	$(GO) build -ldflags="-s -w" -o bin/server ./cmd/server

templ: tools
	templ generate ./web/...

css:
	npx tailwindcss -i ./web/styles/input.css -o ./web/static/dist.css --minify

fmt:
	gofmt -w .

lint:
	$(GO) vet ./...

clean:
	rm -rf bin/ web/static/dist.css web/templates/*_templ.go
