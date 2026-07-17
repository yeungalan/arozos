# Build the Surveillance subservice and stage it where ArozOS discovers it.
#
# ArozOS scans ./subservice/<name>/ relative to the running binary (which lives
# in src/), so the default `deploy` target cross-compiles for the host and drops
# the binary into ../src/subservice/surveillance/ using the required
# <name>_<GOOS>_<GOARCH> naming convention.

NAME    := surveillance
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)
DEPLOY  := ../src/subservice/$(NAME)
BIN     := $(NAME)_$(GOOS)_$(GOARCH)
LDFLAGS := -s -w

.PHONY: build test vet deploy clean all

all: vet test build

build:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -ldflags "$(LDFLAGS)" -o $(BIN) .

test:
	go test ./...

vet:
	go vet ./...

# Stage the freshly built binary into the ArozOS subservice folder. The folder
# is in the repo .gitignore, so built binaries are never committed.
deploy: build
	mkdir -p $(DEPLOY)
	cp $(BIN) $(DEPLOY)/$(BIN)
	@echo "Deployed $(BIN) to $(DEPLOY)/"

clean:
	rm -f $(NAME)_*
