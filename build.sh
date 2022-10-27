#!/usr/bin/env bash
go mod tidy -compat=1.17
go build -o app main.go