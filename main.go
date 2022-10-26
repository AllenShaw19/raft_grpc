package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/AllenShaw19/raft_grpc/httpd"
	"github.com/AllenShaw19/raft_grpc/store"
	"log"
	"net/http"
	"os"
	"os/signal"
)

// Command line defaults
const (
	DefaultHTTPAddr = "localhost:8091"
	DefaultRaftAddr = "localhost:8089"
)

// Command line parameters
var httpAddr string
var raftAddr string
var joinAddr string
var nodeID string

func init() {
	flag.StringVar(&httpAddr, "haddr", DefaultHTTPAddr, "Set the HTTP bind address")
	flag.StringVar(&raftAddr, "raddr", DefaultRaftAddr, "Set Raft bind address")
	flag.StringVar(&joinAddr, "join", "", "Set join address, if any")
	flag.StringVar(&nodeID, "id", "", "Node ID")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [options] <raft-data-path> \n", os.Args[0])
		flag.PrintDefaults()
	}
}

func main() {
	flag.Parse()
	//if flag.NArg() == 0 {
	//	fmt.Fprintf(os.Stderr, "no raft storage directory specified\n")
	//	os.Exit(1)
	//}

	// Ensure Raft storage exists.
	raftDir := "./data"
	if raftDir == "" {
		fmt.Fprintf(os.Stderr, "no raft storage directory specified\n")
		os.Exit(1)
	}
	if err := os.MkdirAll(raftDir, 0700); err != nil {
		log.Fatalf("failed to create dir: %s", err.Error())
	}

	stores := store.NewStores(3, joinAddr == "", nodeID, raftDir, raftAddr)
	// If join was specified, make the join request.
	if joinAddr != "" {
		if err := join(joinAddr, httpAddr, raftAddr, nodeID); err != nil {
			log.Fatalf("failed to join node at %s: %s", joinAddr, err.Error())
		}
	} else {
		log.Println("no join addresses set")
	}

	stores.SetMeta(nodeID, httpAddr)

	h := httpd.New(httpAddr, stores)
	if err := h.Start(); err != nil {
		log.Fatalf("failed to start HTTP service: %s", err.Error())
	}

	log.Println("raftdb started successfully")

	terminate := make(chan os.Signal, 1)
	signal.Notify(terminate, os.Interrupt)
	<-terminate
	log.Println("raftdb exiting")
}

func join(joinAddr, httpAddr, raftAddr, nodeID string) error {
	b, err := json.Marshal(map[string]string{"httpAddr": httpAddr, "raftAddr": raftAddr, "id": nodeID})
	if err != nil {
		return err
	}
	resp, err := http.Post(fmt.Sprintf("http://%s/join", joinAddr), "application-type/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}
