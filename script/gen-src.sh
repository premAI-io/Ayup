#!/bin/sh -eu

protoc \
    --go_out=go/internal \
    --go-grpc_out=go/internal \
    ./grpc/srv/lib.proto \
    --go_opt=paths=source_relative \
    --go-grpc_opt=paths=source_relative

