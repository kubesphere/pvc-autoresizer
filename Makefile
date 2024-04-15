REPO ?= kubesphere
TAG ?= latest

build-local: ; $(info $(M)...Begin to build pvc-autoresizer binary.)  @ ## Build pvc-autoresizer.
	CGO_ENABLED=0 go build -ldflags \
	"-X 'main.goVersion=$(shell go version|sed 's/go version //g')' \
	-X 'main.gitHash=$(shell git describe --dirty --always --tags)' \
	-X 'main.buildTime=$(shell TZ=UTC-8 date +%Y-%m-%d" "%H:%M:%S)'" \
	-o bin/manager main.go

build-image: ; $(info $(M)...Begin to build pvc-autoresizer image.)  @ ## Build pvc-autoresizer image.
	docker build -f Dockerfile -t ${REPO}/pvc-autoresizer:${TAG}  .
	docker push ${REPO}/pvc-autoresizer:${TAG}

build-cross-image: ; $(info $(M)...Begin to build pvc-autoresizer image.)  @ ## Build pvc-autoresizer image.
	docker buildx build -f Dockerfile -t ${REPO}/pvc-autoresizer:${TAG} --push --platform linux/amd64,linux/arm64 .