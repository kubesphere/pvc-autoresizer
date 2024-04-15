# Stage1: Build the pvc-autoresizer binary
FROM golang:1.19 as builder

WORKDIR /workspace
ADD . /workspace/

RUN make build-local

# Stage2: setup runtime container
FROM scratch
WORKDIR /
COPY --from=builder /workspace/bin/manager .
EXPOSE 8080
USER 10000:10000

ENTRYPOINT ["/manager"]
