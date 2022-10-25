package main

import (
	"context"
	"flag"
	"fmt"
	pb "github.com/AllenShaw19/raft_grpc/example/proto"
	"github.com/AllenShaw19/raft_grpc/transport"
	"github.com/hashicorp/raft"
	boltdb "github.com/hashicorp/raft-boltdb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
)

var (
	myAddr   = flag.String("address", "127.0.0.1:50051", "TCP host+port for this node")
	raftId   = flag.String("raft_id", "raft50051", "Node id used by Raft")
	peerAddr = flag.String("peers", "", "Peers hsot + port") // localhost:50052,localhost:50053

	raftDir = flag.String("raft_data_dir", "./data/", "Raft data dir")
)

func main() {
	flag.Parse()

	if *raftId == "" {
		log.Fatalf("flag --raft_id is required")
	}

	ctx := context.Background()
	_, port, err := net.SplitHostPort(*myAddr)
	if err != nil {
		log.Fatalf("failed to parse local address (%q): %v", *myAddr, err)
	}
	sock, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	kv := NewKV()
	r, tm, err := NewRaft(ctx, *raftId, *myAddr, kv)
	if err != nil {
		log.Fatalf("failed to start raft: %v", err)
	}
	s := grpc.NewServer()
	pb.RegisterKVServer(s, &KvServer{
		kv:   kv,
		raft: r,
	})
	tm.Register(s)
	reflection.Register(s)
	if err := s.Serve(sock); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}

func NewRaft(ctx context.Context, myID, myAddress string, fsm raft.FSM) (*raft.Raft, *transport.Manager, error) {
	c := raft.DefaultConfig()
	c.LocalID = raft.ServerID(myID)

	baseDir := filepath.Join(*raftDir, myID)
	err, existed := createDir(baseDir)
	if err != nil {
		return nil, nil, err
	}

	ldb, err := boltdb.NewBoltStore(filepath.Join(baseDir, "logs.dat"))
	if err != nil {
		return nil, nil, fmt.Errorf(`boltdb.NewBoltStore(%q): %v`, filepath.Join(baseDir, "logs.dat"), err)
	}

	sdb, err := boltdb.NewBoltStore(filepath.Join(baseDir, "stable.dat"))
	if err != nil {
		return nil, nil, fmt.Errorf(`boltdb.NewBoltStore(%q): %v`, filepath.Join(baseDir, "stable.dat"), err)
	}

	fss, err := raft.NewFileSnapshotStore(baseDir, 3, os.Stderr)
	if err != nil {
		return nil, nil, fmt.Errorf(`raft.NewFileSnapshotStore(%q, ...): %v`, baseDir, err)
	}

	tm := transport.New(raft.ServerAddress(myAddress), []grpc.DialOption{grpc.WithInsecure()})

	r, err := raft.NewRaft(c, fsm, ldb, sdb, fss, tm.Transport())
	if err != nil {
		return nil, nil, fmt.Errorf("raft.NewRaft: %v", err)
	}
	if !existed {
		peers := make([]string, 0)
		if peerAddr != nil && *peerAddr != "" {
			peers = strings.Split(*peerAddr, ",")
		}
		servers := make([]raft.Server, 0, len(peers)+1)
		servers = append(servers, raft.Server{
			ID:      raft.ServerID(myID),
			Address: raft.ServerAddress(myAddress),
		})
		for _, peer := range peers {
			_, port, err := net.SplitHostPort(peer)
			if err != nil {
				return nil, nil, err
			}
			id := fmt.Sprintf("raft%s", port)
			servers = append(servers, raft.Server{
				Suffrage: raft.Voter,
				ID:       raft.ServerID(id),
				Address:  raft.ServerAddress(peer),
			})
		}

		cfg := raft.Configuration{
			Servers: servers,
		}

		f := r.BootstrapCluster(cfg)
		if err := f.Error(); err != nil {
			return nil, nil, fmt.Errorf("raft.Raft.BootstrapCluster: %v", err)
		}
	}

	return r, tm, nil
}

func createDir(dir string) (error, bool) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return os.MkdirAll(dir, os.ModePerm), false
	}
	return nil, true
}
