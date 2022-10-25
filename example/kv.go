package main

import (
	"context"
	"encoding/json"
	"fmt"
	pb "github.com/AllenShaw19/raft_grpc/example/proto"
	"github.com/AllenShaw19/raft_grpc/rafterrors"
	"github.com/hashicorp/raft"
	"io"
	"io/ioutil"
	"sync"
	"time"
)

type KvStore struct {
	m   map[string]string
	mtx sync.Mutex
}

func NewKV() *KvStore {
	s := &KvStore{}
	s.m = make(map[string]string)
	return s
}

func (s *KvStore) Set(key, value string) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.m[key] = value
}

func (s *KvStore) Get(key string) string {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return s.m[key]
}

func (s *KvStore) Apply(l *raft.Log) interface{} {
	fmt.Println("Apply", string(l.Data))
	req := &pb.SetRequest{}
	err := json.Unmarshal(l.Data, req)
	if err != nil {
		return err
	}
	s.Set(req.Key, req.Value)
	return nil
}

func (s *KvStore) Snapshot() (raft.FSMSnapshot, error) {
	return &snapshot{
		m: clone(s.m),
	}, nil
}

func clone(m map[string]string) map[string]string {
	cp := make(map[string]string)
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

func (s *KvStore) Restore(snapshot io.ReadCloser) error {
	b, err := ioutil.ReadAll(snapshot)
	if err != nil {
		return err
	}
	m := make(map[string]string)
	err = json.Unmarshal(b, &m)
	if err != nil {
		return err
	}
	s.m = m
	return nil
}

type snapshot struct {
	m map[string]string // 这里实际上要进行拷贝
}

func (s *snapshot) Persist(sink raft.SnapshotSink) error {
	val, err := json.Marshal(s.m)
	if err != nil {
		return err
	}

	_, err = sink.Write(val)
	if err != nil {
		sink.Cancel()
		return fmt.Errorf("sink.Write(): %v", err)
	}
	return sink.Close()
}

func (s *snapshot) Release() {
}

type KvServer struct {
	kv   *KvStore
	raft *raft.Raft
}

func (k *KvServer) Set(ctx context.Context, req *pb.SetRequest) (*pb.SetResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	f := k.raft.Apply(data, time.Second)
	if err := f.Error(); err != nil {
		return nil, rafterrors.MarkRetriable(err)
	}
	return &pb.SetResponse{}, nil
}

func (k *KvServer) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	v := k.kv.Get(req.Key)
	return &pb.GetResponse{
		Value:     v,
		ReadIndex: k.raft.AppliedIndex(),
	}, nil
}
