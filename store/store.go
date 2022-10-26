package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	// ErrOpenTimeout is returned when the Store does not apply its initial
	// logs within the specified time.
	ErrOpenTimeout = errors.New("timeout waiting for initial logs application")
)

const (
	retainSnapshotCount = 2
	raftTimeout         = 10 * time.Second
)

type ConsistencyLevel int

const (
	Default ConsistencyLevel = iota
	Stale
	Consistent
)

type command struct {
	Op    string `json:"op,omitempty"`
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
}

type Store struct {
	id   string
	lock sync.Mutex
	m    map[string]string

	raft   *raft.Raft
	logger *log.Logger

	RaftDir  string
	RaftBind string
}

type Stores struct {
	ss []*Store
}

func (s *Stores) Get(id int, key string, lvl ConsistencyLevel) (string, error) {
	return s.ss[id].Get(key, lvl)
}

func (s *Stores) Set(id int, key, value string) error {
	return s.ss[id].Set(key, value)
}

func (s *Stores) Delete(id int, key string) error {
	return s.ss[id].Delete(key)
}

func (s *Stores) Join(nodeID string, httpAddr string, addr string) error {
	for _, ss := range s.ss {
		err := ss.Join(nodeID, httpAddr, addr)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Stores) LeaderAPIAddr(id int) string {
	return s.ss[id].LeaderAPIAddr()
}

func (s *Stores) SetMeta(id string, addr string) {
	for _, e := range s.ss {
		e.SetMeta(id, addr)
	}
}

func NewStores(n int, enableSingle bool, localID, raftDir, raftBind string) *Stores {
	stores := &Stores{}

	ss := make([]*Store, n)
	for i := 0; i < n; i++ {
		s := newStore(fmt.Sprintf("%v", i), raftDir, raftBind)
		err := s.Open(enableSingle, fmt.Sprintf("%s-%d", localID, i))
		if err != nil {
			log.Fatalf("store open fail %v", err)
		}
		ss[i] = s
	}

	stores.ss = ss
	return stores
}

func newStore(id, raftDir, raftBind string) *Store {
	return &Store{
		id:       id,
		m:        make(map[string]string),
		logger:   log.New(os.Stderr, fmt.Sprintf("[store%v]", id), log.LstdFlags),
		RaftDir:  raftDir,
		RaftBind: raftBind,
	}
}

func (s *Store) Open(enableSingle bool, localID string) error {
	// Setup Raft configuration.
	config := raft.DefaultConfig()
	config.LocalID = raft.ServerID(localID)

	newNode := !pathExists(filepath.Join(s.RaftDir, "raft.db"))

	// Setup Raft communication.
	addr, err := net.ResolveTCPAddr("tcp", s.RaftBind)
	if err != nil {
		return err
	}
	transport, err := raft.NewTCPTransport(s.RaftBind, addr, 3, 10*time.Second, os.Stderr)
	if err != nil {
		return err
	}

	// Create the snapshot store. This allows the Raft to truncate the log.
	snapshots, err := raft.NewFileSnapshotStore(s.RaftDir, retainSnapshotCount, os.Stderr)
	if err != nil {
		return fmt.Errorf("file snapshot store: %s", err)
	}

	// Create the log store and stable store.
	var logStore raft.LogStore
	var stableStore raft.StableStore

	boltDB, err := raftboltdb.NewBoltStore(filepath.Join(s.RaftDir, "raft.db"))
	if err != nil {
		return fmt.Errorf("new bolt store: %s", err)
	}
	logStore = boltDB
	stableStore = boltDB

	// Instantiate the Raft systems.
	ra, err := raft.NewRaft(config, (*fsm)(s), logStore, stableStore, snapshots, transport)
	if err != nil {
		return fmt.Errorf("new raft: %s", err)
	}
	s.raft = ra

	if enableSingle && newNode {
		s.logger.Printf("bootstrap needed")
		configuration := raft.Configuration{
			Servers: []raft.Server{
				{
					ID:      config.LocalID,
					Address: transport.LocalAddr(),
				},
			},
		}
		ra.BootstrapCluster(configuration)
	} else {
		s.logger.Printf("no bootstrap needed")
	}

	return nil
}

func (s *Store) LeaderAddr() string {
	return string(s.raft.Leader())
}

// LeaderID returns the node ID of the Raft leader. Returns a
// blank string if there is no leader, or an error.
func (s *Store) LeaderID() (string, error) {
	addr := s.LeaderAddr()
	configFuture := s.raft.GetConfiguration()
	if err := configFuture.Error(); err != nil {
		s.logger.Printf("failed to get raft configuration: %v", err)
		return "", err
	}

	for _, srv := range configFuture.Configuration().Servers {
		if srv.Address == raft.ServerAddress(addr) {
			return string(srv.ID), nil
		}
	}
	return "", nil
}

func (s *Store) LeaderAPIAddr() string {
	id, err := s.LeaderID()
	if err != nil {
		return ""
	}

	addr, err := s.GetMeta(id)
	if err != nil {
		return ""
	}

	return addr
}

// Get returns the value for the given key.
func (s *Store) Get(key string, lvl ConsistencyLevel) (string, error) {
	if lvl != Stale {
		if s.raft.State() != raft.Leader {
			return "", raft.ErrNotLeader
		}

	}

	if lvl == Consistent {
		if err := s.consistentRead(); err != nil {
			return "", err
		}
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	return s.m[key], nil
}

// Set sets the value for the given key.
func (s *Store) Set(key, value string) error {
	if s.raft.State() != raft.Leader {
		return raft.ErrNotLeader
	}

	c := &command{
		Op:    "set",
		Key:   key,
		Value: value,
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}

	f := s.raft.Apply(b, raftTimeout)
	return f.Error()
}

// Delete deletes the given key.
func (s *Store) Delete(key string) error {
	if s.raft.State() != raft.Leader {
		return raft.ErrNotLeader
	}

	c := &command{
		Op:  "delete",
		Key: key,
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}

	f := s.raft.Apply(b, raftTimeout)
	return f.Error()
}

func (s *Store) SetMeta(key, value string) error {
	return s.Set(key, value)
}

func (s *Store) GetMeta(key string) (string, error) {
	return s.Get(key, Stale)
}

func (s *Store) DeleteMeta(key string) error {
	return s.Delete(key)
}

// consistentRead is used to ensure we do not perform a stale
// read. This is done by verifying leadership before the read.
func (s *Store) consistentRead() error {
	future := s.raft.VerifyLeader()
	if err := future.Error(); err != nil {
		return err //fail fast if leader verification fails
	}

	return nil
}

// Join joins a node, identified by nodeID and located at addr, to this store.
// The node must be ready to respond to Raft communications at that address.
func (s *Store) Join(nodeID, httpAddr string, addr string) error {
	s.logger.Printf("received join request for remote node %s at %s", nodeID, addr)

	configFuture := s.raft.GetConfiguration()
	if err := configFuture.Error(); err != nil {
		s.logger.Printf("failed to get raft configuration: %v", err)
		return err
	}

	for _, srv := range configFuture.Configuration().Servers {
		// If a node already exists with either the joining node's ID or address,
		// that node may need to be removed from the config first.
		if srv.ID == raft.ServerID(nodeID) || srv.Address == raft.ServerAddress(addr) {
			// However if *both* the ID and the address are the same, then nothing -- not even
			// a join operation -- is needed.
			if srv.Address == raft.ServerAddress(addr) && srv.ID == raft.ServerID(nodeID) {
				s.logger.Printf("node %s at %s already member of cluster, ignoring join request", nodeID, addr)
				return nil
			}

			future := s.raft.RemoveServer(srv.ID, 0, 0)
			if err := future.Error(); err != nil {
				return fmt.Errorf("error removing existing node %s at %s: %s", nodeID, addr, err)
			}
		}
	}

	f := s.raft.AddVoter(raft.ServerID(nodeID), raft.ServerAddress(addr), 0, 0)
	if f.Error() != nil {
		return f.Error()
	}

	// Set meta info
	if err := s.SetMeta(nodeID, httpAddr); err != nil {
		return err
	}

	s.logger.Printf("node %s at %s joined successfully", nodeID, addr)
	return nil
}
