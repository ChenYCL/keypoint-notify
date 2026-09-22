BINARY := kp
PREFIX ?= $(HOME)/.local
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

LDFLAGS := -s -w -X github.com/light/keypoint-notify/internal/cli.Version=$(VERSION)

.PHONY: help build install test vet fmt lint run clean skill uninstall

help: ## 显示可用目标
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

build: ## 编译单二进制到 ./kp
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/keypoint

install: build ## 装到 $(PREFIX)/bin
	@mkdir -p $(PREFIX)/bin
	cp $(BINARY) $(PREFIX)/bin/$(BINARY)
	@echo "已安装 $(PREFIX)/bin/$(BINARY)"

test: ## 跑全部测试
	go test ./... -count=1

vet: ## go vet
	go vet ./...

fmt: ## gofmt
	gofmt -w $$(find . -name '*.go' -not -path './.git/*')

check: vet test ## vet + test

run: build ## 本地起服务（数据在 ./data）
	./$(BINARY) serve

skill: ## 把 skill 链接到 ~/.claude/skills/keypoint-notify
	@mkdir -p $(HOME)/.claude/skills
	@rm -rf $(HOME)/.claude/skills/keypoint-notify
	ln -s $(CURDIR)/skills/keypoint-notify $(HOME)/.claude/skills/keypoint-notify
	@echo "已链接 → $(HOME)/.claude/skills/keypoint-notify"
	@echo '在 Claude Code 里说「上报一下」或「记个任务」就会触发'

uninstall: ## 卸载二进制与 skill 链接
	rm -f $(PREFIX)/bin/$(BINARY)
	rm -rf $(HOME)/.claude/skills/keypoint-notify

clean: ## 清掉编译产物
	rm -f $(BINARY)
	go clean -testcache
