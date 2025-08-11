BINARY_NAME=dbterm

.PHONY: all build clean test release

all: build

build:
	go build -o $(BINARY_NAME) .

clean:
	rm -f $(BINARY_NAME)
	rm -rf dist/

test:
	go test ./...

release:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 go build -o dist/$(BINARY_NAME)-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build -o dist/$(BINARY_NAME)-linux-arm64 .
	GOOS=darwin GOARCH=amd64 go build -o dist/$(BINARY_NAME)-darwin-amd64 .
	GOOS=darwin GOARCH=arm64 go build -o dist/$(BINARY_NAME)-darwin-arm64 .
	GOOS=windows GOARCH=amd64 go build -o dist/$(BINARY_NAME)-windows-amd64.exe .
