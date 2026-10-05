# macOS Intel (amd64)
GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.coreVersion=v3.1.19" -o darwin/amd64/corectl ./cmd/corectl

# macOS Apple Silicon (arm64)  
GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.coreVersion=v3.1.19" -o darwin/arm64/corectl ./cmd/corectl

# Linux (可选，方便 Linux 用户)
GOOS=linux GOARCH=amd64 go build -ldflags "-X main.coreVersion=v3.1.19" -o corectl_linux_amd64 ./cmd/corectl