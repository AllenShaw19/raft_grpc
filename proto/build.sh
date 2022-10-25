#!/usr/bin/env bash
protoc raft.proto --go_out=plugins=grpc:. --go_opt=paths=source_relative
