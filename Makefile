BINARY := vidfetch
VERSION ?= 1.1.0
LDFLAGS := -s -w

.PHONY: all build linux run test vet clean docker deploy

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

run: build
	VF_ADMIN_PASS=dev123 VF_SAVE_DIRS="./downloads:本地" ./$(BINARY) -addr :8080 -data ./data

vet:
	go vet ./...
	gofmt -l .

test:
	go test ./...

docker:
	docker build -t vidfetch:$(VERSION) .

clean:
	rm -f $(BINARY)
