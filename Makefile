#
# Author: Markus Stenberg <fingon@iki.fi>
#
# Copyright (c) 2025 Markus Stenberg
#
# Created:       Sun Apr 13 08:23:25 2025 mstenber
# Last modified: Wed Apr 16 17:01:26 2025 mstenber
# Edit time:     2 min
#
#

GO_TEST_TARGET=./...

.PHONY: ci
ci: lint test

.PHONY: test
test:
	go test $(GO_TEST_TARGET)

# See https://golangci-lint.run/usage/linters/
.PHONY: lint
lint:
	golangci-lint run --fix  # Externally installed, e.g. brew
