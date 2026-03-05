BIN := dispatch
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build install clean cross

build:
	go build -ldflags "-s -w" -o $(BIN) .

install: build
	cp $(BIN) /usr/local/bin/$(BIN)

clean:
	rm -f $(BIN) dispatch-linux-amd64

# Cross-compile for Linux (for Clawdia)
linux:
	GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o dispatch-linux-amd64 .
