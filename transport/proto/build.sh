#!/usr/bin/env bash
protoc raft.proto --go-grpc_out=. --go-grpc_opt=paths=source_relative
