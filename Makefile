.PHONY: test test-no-cache test-stores test-stores-no-cache test-integration-all test-integration-all-no-cache test-integration-stores test-integration-stores-no-cache test-integration-postgres test-integration-postgres-no-cache

GO ?= go
GOTOOLCHAIN ?= local
INTEGRATION_TAG ?= integration
MOD_MODE ?= mod
TEST_TIMEOUT ?= 30m
TEST_FLAGS ?=
GO_ENV ?= env -u GOROOT GOTOOLCHAIN=$(GOTOOLCHAIN)

test:
	$(GO_ENV) $(GO) test -mod=$(MOD_MODE) -timeout=$(TEST_TIMEOUT) $(TEST_FLAGS) ./...

test-no-cache:
	$(GO_ENV) $(GO) test -count=1 -mod=$(MOD_MODE) -timeout=$(TEST_TIMEOUT) $(TEST_FLAGS) ./...

test-stores:
	$(GO_ENV) $(GO) test -mod=$(MOD_MODE) -timeout=$(TEST_TIMEOUT) $(TEST_FLAGS) ./stores/...

test-stores-no-cache:
	$(GO_ENV) $(GO) test -count=1 -mod=$(MOD_MODE) -timeout=$(TEST_TIMEOUT) $(TEST_FLAGS) ./stores/...

test-integration-all:
	$(GO_ENV) $(GO) test -mod=$(MOD_MODE) -tags=$(INTEGRATION_TAG) -timeout=$(TEST_TIMEOUT) $(TEST_FLAGS) ./...

test-integration-all-no-cache:
	$(GO_ENV) $(GO) test -count=1 -mod=$(MOD_MODE) -tags=$(INTEGRATION_TAG) -timeout=$(TEST_TIMEOUT) $(TEST_FLAGS) ./...

test-integration-stores:
	$(GO_ENV) $(GO) test -mod=$(MOD_MODE) -tags=$(INTEGRATION_TAG) -timeout=$(TEST_TIMEOUT) $(TEST_FLAGS) ./stores/...

test-integration-stores-no-cache:
	$(GO_ENV) $(GO) test -count=1 -mod=$(MOD_MODE) -tags=$(INTEGRATION_TAG) -timeout=$(TEST_TIMEOUT) $(TEST_FLAGS) ./stores/...

test-integration-postgres:
	$(GO_ENV) $(GO) test -mod=$(MOD_MODE) -tags=$(INTEGRATION_TAG) -timeout=$(TEST_TIMEOUT) $(TEST_FLAGS) ./stores/postgres

test-integration-postgres-no-cache:
	$(GO_ENV) $(GO) test -count=1 -mod=$(MOD_MODE) -tags=$(INTEGRATION_TAG) -timeout=$(TEST_TIMEOUT) $(TEST_FLAGS) ./stores/postgres
