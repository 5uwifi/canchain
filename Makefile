# This Makefile is meant to be used by people that do not usually work
# with Go source code. If you know what GOPATH is then you probably
# don't need to bother with make.

.PHONY: can can-cross swarm evm all test clean
.PHONY: can-linux can-linux-386 can-linux-amd64 can-linux-mips64 can-linux-mips64le
.PHONY: can-linux-arm can-linux-arm-5 can-linux-arm-6 can-linux-arm-7 can-linux-arm64
.PHONY: can-darwin can-darwin-386 can-darwin-amd64
.PHONY: can-windows can-windows-386 can-windows-amd64

GOBIN = $(shell pwd)/build/bin
GO ?= latest

can:
	build/env.sh go run build/ci.go install ./helper/can
	@echo "Done building."
	@echo "Run \"$(GOBIN)/can\" to launch can."

swarm:
	build/env.sh go run build/ci.go install ./helper/swarm
	@echo "Done building."
	@echo "Run \"$(GOBIN)/swarm\" to launch swarm."

all:
	build/env.sh go run build/ci.go install

test: all
	build/env.sh go run build/ci.go test

lint: ## Run linters.
	build/env.sh go run build/ci.go lint

clean:
	rm -fr build/_workspace/pkg/ $(GOBIN)/*

# The devtools target installs tools required for 'go generate'.
# You need to put $GOBIN (or $GOPATH/bin) in your PATH to use 'go generate'.

devtools:
	env GOBIN= go get -u golang.org/x/tools/cmd/stringer
	env GOBIN= go get -u github.com/kevinburke/go-bindata/go-bindata
	env GOBIN= go get -u github.com/fjl/gencodec
	env GOBIN= go get -u github.com/golang/protobuf/protoc-gen-go
	env GOBIN= go install ./helper/abigen
	@type "npm" 2> /dev/null || echo 'Please install node.js and npm'
	@type "solc" 2> /dev/null || echo 'Please install solc'
	@type "protoc" 2> /dev/null || echo 'Please install protoc'

# Cross Compilation Targets (xgo)

can-cross: can-linux can-darwin can-windows
	@echo "Full cross compilation done:"
	@ls -ld $(GOBIN)/can-*

can-linux: can-linux-386 can-linux-amd64 can-linux-arm can-linux-mips64 can-linux-mips64le
	@echo "Linux cross compilation done:"
	@ls -ld $(GOBIN)/can-linux-*

can-linux-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/386 -v ./helper/can
	@echo "Linux 386 cross compilation done:"
	@ls -ld $(GOBIN)/can-linux-* | grep 386

can-linux-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/amd64 -v ./helper/can
	@echo "Linux amd64 cross compilation done:"
	@ls -ld $(GOBIN)/can-linux-* | grep amd64

can-linux-arm: can-linux-arm-5 can-linux-arm-6 can-linux-arm-7 can-linux-arm64
	@echo "Linux ARM cross compilation done:"
	@ls -ld $(GOBIN)/can-linux-* | grep arm

can-linux-arm-5:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-5 -v ./helper/can
	@echo "Linux ARMv5 cross compilation done:"
	@ls -ld $(GOBIN)/can-linux-* | grep arm-5

can-linux-arm-6:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-6 -v ./helper/can
	@echo "Linux ARMv6 cross compilation done:"
	@ls -ld $(GOBIN)/can-linux-* | grep arm-6

can-linux-arm-7:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm-7 -v ./helper/can
	@echo "Linux ARMv7 cross compilation done:"
	@ls -ld $(GOBIN)/can-linux-* | grep arm-7

can-linux-arm64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/arm64 -v ./helper/can
	@echo "Linux ARM64 cross compilation done:"
	@ls -ld $(GOBIN)/can-linux-* | grep arm64

can-linux-mips:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips --ldflags '-extldflags "-static"' -v ./helper/can
	@echo "Linux MIPS cross compilation done:"
	@ls -ld $(GOBIN)/can-linux-* | grep mips

can-linux-mipsle:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mipsle --ldflags '-extldflags "-static"' -v ./helper/can
	@echo "Linux MIPSle cross compilation done:"
	@ls -ld $(GOBIN)/can-linux-* | grep mipsle

can-linux-mips64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips64 --ldflags '-extldflags "-static"' -v ./helper/can
	@echo "Linux MIPS64 cross compilation done:"
	@ls -ld $(GOBIN)/can-linux-* | grep mips64

can-linux-mips64le:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=linux/mips64le --ldflags '-extldflags "-static"' -v ./helper/can
	@echo "Linux MIPS64le cross compilation done:"
	@ls -ld $(GOBIN)/can-linux-* | grep mips64le

can-darwin: can-darwin-386 can-darwin-amd64
	@echo "Darwin cross compilation done:"
	@ls -ld $(GOBIN)/can-darwin-*

can-darwin-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=darwin/386 -v ./helper/can
	@echo "Darwin 386 cross compilation done:"
	@ls -ld $(GOBIN)/can-darwin-* | grep 386

can-darwin-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=darwin/amd64 -v ./helper/can
	@echo "Darwin amd64 cross compilation done:"
	@ls -ld $(GOBIN)/can-darwin-* | grep amd64

can-windows: can-windows-386 can-windows-amd64
	@echo "Windows cross compilation done:"
	@ls -ld $(GOBIN)/can-windows-*

can-windows-386:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=windows/386 -v ./helper/can
	@echo "Windows 386 cross compilation done:"
	@ls -ld $(GOBIN)/can-windows-* | grep 386

can-windows-amd64:
	build/env.sh go run build/ci.go xgo -- --go=$(GO) --targets=windows/amd64 -v ./helper/can
	@echo "Windows amd64 cross compilation done:"
	@ls -ld $(GOBIN)/can-windows-* | grep amd64
