BINARY_NAME=dbterm
GO_BUILD_FLAGS=-trimpath -ldflags="-s -w"
VERSION?=0.0.0

.PHONY: all build clean test release release-core release-ios deb apt-repo

all: build

build:
	go build $(GO_BUILD_FLAGS) -o $(BINARY_NAME) .

clean:
	rm -f $(BINARY_NAME)
	rm -rf dist/

test:
	go test ./...

release: release-core release-ios

deb: release-core
	./scripts/build-deb.sh $(VERSION) dist

apt-repo: deb
	./scripts/build-apt-repo.sh dist/apt dist

release-core:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 go build $(GO_BUILD_FLAGS) -o dist/$(BINARY_NAME)-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build $(GO_BUILD_FLAGS) -o dist/$(BINARY_NAME)-linux-arm64 .
	GOOS=darwin GOARCH=amd64 go build $(GO_BUILD_FLAGS) -o dist/$(BINARY_NAME)-darwin-amd64 .
	GOOS=darwin GOARCH=arm64 go build $(GO_BUILD_FLAGS) -o dist/$(BINARY_NAME)-darwin-arm64 .
	GOOS=windows GOARCH=amd64 go build $(GO_BUILD_FLAGS) -o dist/$(BINARY_NAME)-windows-amd64.exe .

release-ios:
	mkdir -p dist
	@if [ "$$(uname -s)" != "Darwin" ]; then \
		echo "Skipping iOS build (requires macOS + Xcode CLI tools)."; \
	else \
		SDKROOT="$$(xcrun --sdk iphoneos --show-sdk-path)"; \
		CC="$$(xcrun --sdk iphoneos --find clang)"; \
		GOOS=ios GOARCH=arm64 CGO_ENABLED=1 CC="$$CC" SDKROOT="$$SDKROOT" \
		CGO_CFLAGS="-isysroot $$SDKROOT -miphoneos-version-min=13.0" \
		CGO_LDFLAGS="-isysroot $$SDKROOT -miphoneos-version-min=13.0" \
		go build -trimpath -buildmode=c-archive -ldflags="-s -w" -o dist/$(BINARY_NAME)-ios-arm64.a .; \
	fi
