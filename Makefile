.PHONY: build test test-mysql gocyclo golangci clean

build:
	@echo "Building armature-health..."
	@go build ./...

test:
	@echo "Running unit tests..."
	@go test -v ./...

# test-mysql also runs the SQL store tests against MySQL/MariaDB, in a throwaway container
# (podman or docker). Override MYSQL_IMAGE, e.g. docker.io/library/mysql:8.4.
CONTAINER ?= $(shell command -v podman >/dev/null 2>&1 && echo podman || echo docker)
MYSQL_IMAGE ?= docker.io/library/mariadb:11
MYSQL_CONTAINER := armature-health-test-mysql

test-mysql:
	@echo "Running tests against $(MYSQL_IMAGE)..."
	@$(CONTAINER) run -d --rm --name $(MYSQL_CONTAINER) -e MARIADB_ROOT_PASSWORD=pw -e MYSQL_ROOT_PASSWORD=pw -p 127.0.0.1:13306:3306 $(MYSQL_IMAGE) >/dev/null
	@for i in $$(seq 120); do \
		$(CONTAINER) exec $(MYSQL_CONTAINER) sh -c 'mariadb-admin ping -h 127.0.0.1 -ppw --silent || mysqladmin ping -h 127.0.0.1 -ppw --silent' >/dev/null 2>&1 && break; \
		sleep 1; \
	done
	@HEALTH_TEST_MYSQL_DSN='root:pw@tcp(127.0.0.1:13306)/' go test -count=1 ./...; \
		status=$$?; $(CONTAINER) stop $(MYSQL_CONTAINER) >/dev/null; exit $$status

gocyclo:
	@echo "Running gocyclo..."
	@if gocyclo -over 5 -ignore "_test" . | grep -qE "^[0-9]+"; then \
		gocyclo -over 5 -ignore "_test" .; \
		echo "Error: Functions with complexity > 5 found in library code"; \
		exit 1; \
	fi
	@echo "All library functions have complexity <= 5"

golangci:
	golangci-lint run

clean:
	@echo "Cleaning build artifacts..."
	@go clean ./...
