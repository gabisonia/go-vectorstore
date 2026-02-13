.PHONY: test-integration-all test-integration-stores test-integration-postgres test-integration-mssql test-integration-msql

GO ?= go
GOTOOLCHAIN ?= local
INTEGRATION_TAG ?= integration
MOD_MODE ?= mod
TEST_TIMEOUT ?= 30m
TEST_FLAGS ?=
GO_ENV ?= env -u GOROOT GOTOOLCHAIN=$(GOTOOLCHAIN)

test-integration-all:
	$(GO_ENV) $(GO) test -mod=$(MOD_MODE) -tags=$(INTEGRATION_TAG) -timeout=$(TEST_TIMEOUT) $(TEST_FLAGS) ./...

test-integration-stores:
	$(GO_ENV) $(GO) test -mod=$(MOD_MODE) -tags=$(INTEGRATION_TAG) -timeout=$(TEST_TIMEOUT) $(TEST_FLAGS) ./stores/...

test-integration-postgres:
	$(GO_ENV) $(GO) test -mod=$(MOD_MODE) -tags=$(INTEGRATION_TAG) -timeout=$(TEST_TIMEOUT) $(TEST_FLAGS) ./stores/postgres

test-integration-mssql:
	$(GO_ENV) $(GO) test -mod=$(MOD_MODE) -tags=$(INTEGRATION_TAG) -timeout=$(TEST_TIMEOUT) $(TEST_FLAGS) ./stores/mssql

test-integration-msql: test-integration-mssql
