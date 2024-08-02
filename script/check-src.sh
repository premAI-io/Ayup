#!/bin/sh -eu

(cd go && golangci-lint run)

need_fmt=$(gofmt -l -s ./go | wc -l)
if [ "$need_fmt" -gt 0 ]; then
    echo Files need formatting: $need_fmt, run '"gofmt -w -s ."'
    exit 1
fi
