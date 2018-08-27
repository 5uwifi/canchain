# This Makefile is meant to be used by people that do not usually work
# with Go source code. If you know what GOPATH is then you probably
# don't need to bother with make.

.PHONY: gcan gcan-cross all test clean
.PHONY: gcan-linux gcan-linux-386 gcan-linux-amd64 gcan-linux-mips64 gcan-linux-mips64le
.PHONY: gcan-linux-arm gcan-linux-arm-5 gcan-linux-arm-6 gcan-linux-arm-7 gcan-linux-arm64
.PHONY: gcan-darwin gcan-darwin-386 gcan-darwin-amd64
.PHONY: gcan-windows gcan-windows-386 gcan-windows-amd64

GOBIN = $(shell pwd)/build/bin
GO ?= latest

gcan:
	build/env.sh go run build/ci.go install ./cmd/gcan
	@echo "Done building."
	@echo "Run \"$(GOBIN)/gcan\" to launch gcan."

all:
	build/env.sh go run build/ci.go install

test: all
	build/env.sh go run build/ci.go test

lint: ## Run linters.
	build/env.sh go run build/ci.go lint

clean:
	./build/clean_go_build_cache.sh
	rm -fr build/_workspace/pkg/ $(GOBIN)/*

# The devtools target installs tools required for 'go generate'.
# You need to put $GOBIN (or $GOPATH/bin) in your PATH to use 'go generate'.

devtools:
	env GOBIN= go get -u golang.org/x/tools/cmd/stringer
	env GOBIN= go get -u github.com/kevinburke/go-bindata/go-bindata
	env GOBIN= go get -u github.com/fjl/gencodec
	env GOBIN= go get -u github.com/golang/protobuf/protoc-gen-go
	env GOBIN= go install ./cmd/abigen
	@type "npm" 2> /dev/null || echo 'Please install node.js and npm'
	@type "solc" 2> /dev/null || echo 'Please install solc'
	@type "protoc" 2> /dev/null || echo 'Please install protoc'

# Cross Compilation Targets (xgo)

gcan-cross: gcan-linux gcan-darwin gcan-windows
	@echo "Full cross compilation done:"
	@ls -ld $(GOBIN)/gcan-*

gcan-linux: gcan-linux-386 gcan-linux-amd64 gcan-linux-arm gcan-linux-mips64 gcan-linux-mips64le
	@echo "Linux cross compilation done:"
	@ls -ld $(GOBIN)/gcan-linux-*

gcan-linux-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/386 -v ./cmd/gcan
	@echo "Linux 386 cross compilation done:"
	@ls -ld $(GOBIN)/gcan-linux-* | grep 386

gcan-linux-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/amd64 -v ./cmd/gcan
	@echo "Linux amd64 cross compilation done:"
	@ls -ld $(GOBIN)/gcan-linux-* | grep amd64

gcan-linux-arm: gcan-linux-arm-5 gcan-linux-arm-6 gcan-linux-arm-7 gcan-linux-arm64
	@echo "Linux ARM cross compilation done:"
	@ls -ld $(GOBIN)/gcan-linux-* | grep arm

gcan-linux-arm-5:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-5 -v ./cmd/gcan
	@echo "Linux ARMv5 cross compilation done:"
	@ls -ld $(GOBIN)/gcan-linux-* | grep arm-5

gcan-linux-arm-6:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-6 -v ./cmd/gcan
	@echo "Linux ARMv6 cross compilation done:"
	@ls -ld $(GOBIN)/gcan-linux-* | grep arm-6

gcan-linux-arm-7:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-7 -v ./cmd/gcan
	@echo "Linux ARMv7 cross compilation done:"
	@ls -ld $(GOBIN)/gcan-linux-* | grep arm-7

gcan-linux-arm64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm64 -v ./cmd/gcan
	@echo "Linux ARM64 cross compilation done:"
	@ls -ld $(GOBIN)/gcan-linux-* | grep arm64

gcan-linux-mips:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips --ldflags '-extldflags "-static"' -v ./cmd/gcan
	@echo "Linux MIPS cross compilation done:"
	@ls -ld $(GOBIN)/gcan-linux-* | grep mips

gcan-linux-mipsle:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mipsle --ldflags '-extldflags "-static"' -v ./cmd/gcan
	@echo "Linux MIPSle cross compilation done:"
	@ls -ld $(GOBIN)/gcan-linux-* | grep mipsle

gcan-linux-mips64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips64 --ldflags '-extldflags "-static"' -v ./cmd/gcan
	@echo "Linux MIPS64 cross compilation done:"
	@ls -ld $(GOBIN)/gcan-linux-* | grep mips64

gcan-linux-mips64le:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips64le --ldflags '-extldflags "-static"' -v ./cmd/gcan
	@echo "Linux MIPS64le cross compilation done:"
	@ls -ld $(GOBIN)/gcan-linux-* | grep mips64le

gcan-darwin: gcan-darwin-386 gcan-darwin-amd64
	@echo "Darwin cross compilation done:"
	@ls -ld $(GOBIN)/gcan-darwin-*

gcan-darwin-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=darwin/386 -v ./cmd/gcan
	@echo "Darwin 386 cross compilation done:"
	@ls -ld $(GOBIN)/gcan-darwin-* | grep 386

gcan-darwin-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=darwin/amd64 -v ./cmd/gcan
	@echo "Darwin amd64 cross compilation done:"
	@ls -ld $(GOBIN)/gcan-darwin-* | grep amd64

gcan-windows: gcan-windows-386 gcan-windows-amd64
	@echo "Windows cross compilation done:"
	@ls -ld $(GOBIN)/gcan-windows-*

gcan-windows-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=windows/386 -v ./cmd/gcan
	@echo "Windows 386 cross compilation done:"
	@ls -ld $(GOBIN)/gcan-windows-* | grep 386

gcan-windows-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=windows/amd64 -v ./cmd/gcan
	@echo "Windows amd64 cross compilation done:"
	@ls -ld $(GOBIN)/gcan-windows-* | grep amd64
