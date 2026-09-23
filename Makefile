BINARY := kp
PREFIX ?= $(HOME)/.local
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

LDFLAGS := -s -w -X github.com/ChenYCL/keypoint-notify/internal/cli.Version=$(VERSION)

.PHONY: help build install test vet fmt lint run clean skill uninstall check sync-skill dist release-bin

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

sync-skill: ## 把 skills/ 同步到内嵌副本（改完 skill 必跑）
	@rm -rf internal/skill/assets
	@mkdir -p internal/skill/assets
	@cp -r skills/keypoint-notify/. internal/skill/assets/
	@echo "已同步 skills/keypoint-notify → internal/skill/assets"

skill: sync-skill ## 把 skill 链接到 ~/.claude/skills/keypoint-notify
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

DIST_PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64

dist: ## 交叉编译四个平台到 dist/（给 install.sh 用）
	@mkdir -p dist
	@for t in $(DIST_PLATFORMS); do \
	  goos=$${t%%/*}; goarch=$${t##*/}; \
	  CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch go build -trimpath \
	    -ldflags="$(LDFLAGS)" -o dist/kp-$$goos-$$goarch ./cmd/keypoint & \
	done; wait
	@# 并行编译时一个失败 wait 也返回 0 —— 逐个确认产物在
	@for t in $(DIST_PLATFORMS); do test -s dist/kp-$${t%%/*}-$${t##*/} || { echo "缺 kp-$${t%%/*}-$${t##*/}"; exit 1; }; done
	@cd dist && (command -v sha256sum >/dev/null && sha256sum kp-* || shasum -a 256 kp-*) > SHA256SUMS
	@ls -lh dist/ | tail -n +2
	@echo "把 dist/ 放到服务端二进制旁边的 bin/ 目录，install.sh 就能按平台发对应版本"

release-bin: dist ## dist/ 移到当前二进制的同级 bin/
	@mkdir -p $(PREFIX)/bin/bin
	@cp dist/* $(PREFIX)/bin/bin/
	@echo "已放到 $(PREFIX)/bin/bin/"
