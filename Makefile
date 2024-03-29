GIT_COMMIT=$(shell git rev-parse HEAD | head -c 7)
IMG ?= kubespheredev/pvc-autoresizer:${GIT_COMMIT}
IMGLATEST ?= kubespheredev/pvc-autoresizer:latest

.PHONY: build
build:
	go mod tidy && go mod verify && go build -o bin/manager main.go

.PHONY: docker-build
docker-build: #test ## Build docker image with the manager.
	docker build -t ${IMG} .

.PHONY: docker-push
docker-push:
	docker push ${IMG}
	docker tag ${IMG} ${IMGLATEST}
	docker push ${IMGLATEST}