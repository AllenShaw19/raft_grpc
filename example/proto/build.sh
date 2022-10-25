#!/usr/bin/env bash
protoc kv.proto --go_out=plugins=grpc:. --go_opt=paths=source_relative
