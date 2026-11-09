APP      := mediasuper
MODULE   := mediasuper
PKG      := ./cmd/mediasuper

# 版本号以 internal/version/version.go 里的默认值为准，构建时可用 VERSION= 覆盖
VERSION  ?= $(shell sed -n 's/^[[:space:]]*Version[[:space:]]*=[[:space:]]*"\(.*\)"/\1/p' internal/version/version.go | head -1)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
BUILT_AT ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.BuildTime=$(BUILT_AT)

GO ?= go

.PHONY: all build run test vet fmt tidy clean \
        cross docker docker-multiarch fpk release

all: build

## build: 本机平台构建到 dist/
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(APP) $(PKG)
	@echo "==> dist/$(APP) ($(VERSION))"

## run: 本地开发启动（数据放 ./data，下载放 ./downloads）
run: build
	MS_ADMIN_PASS=losfer MS_SAVE_DIRS="./downloads:本地" ./dist/$(APP) -addr :8080 -data ./data

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...
	@test -z "$$(gofmt -l $$(git ls-files '*.go'))" || (echo "需要 gofmt："; gofmt -l $$(git ls-files '*.go'); exit 1)

fmt:
	gofmt -w $$(git ls-files '*.go')

tidy:
	$(GO) mod tidy

## cross: 交叉编译 linux/amd64、arm64、armv7、armv6、386
cross:
	@for t in linux/amd64 linux/arm64 linux/armv7 linux/armv6 linux/386; do \
		os=$${t%/*}; arch=$${t#*/}; goarm=""; \
		case $$arch in armv6) goarm=6; arch=arm;; armv7) goarm=7; arch=arm;; esac; \
		out=dist/$(APP)-$$os-$${t#*/}; \
		echo "==> $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch GOARM=$$goarm \
			$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $$out $(PKG) || exit 1; \
	done

## release: 交叉编译 + 压缩 + 校验和 + 更新 version.json / 版本列表
release:
	VERSION=$(VERSION) ./scripts/build-release.sh

## fpk: 生成飞牛 fnOS 安装包（需要 fnpack，见 docs/FNOS.md）
fpk:
	VERSION=$(VERSION) ./scripts/build-fpk.sh

## docker: 本机架构镜像
docker:
	docker build -f deploy/docker/Dockerfile \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		-t $(APP):$(VERSION) -t $(APP):latest .

## docker-multiarch: 多架构镜像（需要 buildx + 已登录 registry）
docker-multiarch:
	docker buildx build -f deploy/docker/Dockerfile \
		--platform linux/amd64,linux/arm64,linux/arm/v7 \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		-t ghcr.io/mr-losfer/media-super:$(VERSION) \
		-t ghcr.io/mr-losfer/media-super:latest \
		--push .

clean:
	rm -rf dist build *.fpk
