package transport

import (
	"context"
	pb "github.com/AllenShaw19/raft_grpc/proto"
	"github.com/AllenShaw19/raft_grpc/transport/codec"
	"github.com/hashicorp/raft"
	"google.golang.org/grpc"
	"io"
	"sync"
	"time"
)

type conn struct {
	clientConn *grpc.ClientConn
	client     pb.RaftClient
	mtx        sync.Mutex
}

type appendFuture struct {
	raft.AppendFuture

	start    time.Time
	request  *raft.AppendEntriesRequest
	response raft.AppendEntriesResponse
	err      error
	done     chan struct{}
}

// Error blocks until the future arrives and then
// returns the error status of the future.
// This may be called any number of times - all
// calls will return the same value.
// Note that it is not OK to call this method
// twice concurrently on the same Future instance.
func (f *appendFuture) Error() error {
	<-f.done
	return f.err
}

// Start returns the time that the append request was started.
// It is always OK to call this method.
func (f *appendFuture) Start() time.Time {
	return f.start
}

// Request holds the parameters of the AppendEntries call.
// It is always OK to call this method.
func (f *appendFuture) Request() *raft.AppendEntriesRequest {
	return f.request
}

// Response holds the results of the AppendEntries call.
// This method must only be called after the Error
// method returns, and will only be valid on success.
func (f *appendFuture) Response() *raft.AppendEntriesResponse {
	return &f.response
}

type raftPipelineAPI struct {
	stream        pb.Raft_AppendEntriesPipelineClient
	cancel        func()
	inflightChMtx sync.Mutex
	inflightCh    chan *appendFuture
	doneCh        chan raft.AppendFuture
}

func (r *raftPipelineAPI) AppendEntries(req *raft.AppendEntriesRequest, resp *raft.AppendEntriesResponse) (raft.AppendFuture, error) {
	af := &appendFuture{
		start:   time.Now(),
		request: req,
		done:    make(chan struct{}),
	}
	if err := r.stream.Send(codec.EncodeAppendEntriesRequest(req)); err != nil {
		return nil, err
	}
	r.inflightChMtx.Lock()
	select {
	case <-r.stream.Context().Done():
	default:
		r.inflightCh <- af
	}
	r.inflightChMtx.Unlock()
	return af, nil
}

func (r *raftPipelineAPI) Consumer() <-chan raft.AppendFuture {
	return r.doneCh
}

func (r *raftPipelineAPI) Close() error {
	r.cancel()
	r.inflightChMtx.Lock()
	close(r.inflightCh)
	r.inflightChMtx.Unlock()
	return nil
}

func (r *raftPipelineAPI) receiver() {
	for af := range r.inflightCh {
		msg, err := r.stream.Recv()
		if err != nil {
			af.err = err
		} else {
			af.response = *codec.DecodeAppendEntriesResponse(msg)
		}
		close(af.done)
		r.doneCh <- af
	}
}

type raftApi struct {
	m *Manager
}

func (r raftApi) Consumer() <-chan raft.RPC {
	return r.m.rpcChan
}

func (r raftApi) LocalAddr() raft.ServerAddress {
	return r.m.localAddress
}

func (r raftApi) AppendEntriesPipeline(id raft.ServerID, target raft.ServerAddress) (raft.AppendPipeline, error) {
	ctx := context.Background()
	c, err := r.getPeer(id, target)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	stream, err := c.AppendEntriesPipeline(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	rpa := &raftPipelineAPI{
		stream:     stream,
		cancel:     cancel,
		inflightCh: make(chan *appendFuture, 20),
		doneCh:     make(chan raft.AppendFuture, 20),
	}
	go rpa.receiver()
	return rpa, nil
}

func (r raftApi) AppendEntries(id raft.ServerID, target raft.ServerAddress, args *raft.AppendEntriesRequest, resp *raft.AppendEntriesResponse) error {
	ctx := context.Background()
	c, err := r.getPeer(id, target)
	if err != nil {
		return err
	}
	req := codec.EncodeAppendEntriesRequest(args)
	ret, err := c.AppendEntries(ctx, req)
	if err != nil {
		return err
	}
	resp = codec.DecodeAppendEntriesResponse(ret)
	return nil
}

func (r raftApi) RequestVote(id raft.ServerID, target raft.ServerAddress, args *raft.RequestVoteRequest, resp *raft.RequestVoteResponse) error {
	ctx := context.Background()
	c, err := r.getPeer(id, target)
	if err != nil {
		return err
	}
	ret, err := c.RequestVote(ctx, codec.EncodeRequestVoteRequest(args))
	if err != nil {
		return err
	}
	resp = codec.DecodeRequestVoteResponse(ret)
	return nil
}

func (r raftApi) InstallSnapshot(id raft.ServerID, target raft.ServerAddress, args *raft.InstallSnapshotRequest, resp *raft.InstallSnapshotResponse, data io.Reader) error {
	ctx := context.Background()

	c, err := r.getPeer(id, target)
	if err != nil {
		return err
	}
	stream, err := c.InstallSnapshot(ctx)
	if err != nil {
		return err
	}
	if err := stream.Send(codec.EncodeInstallSnapshotRequest(args)); err != nil {
		return err
	}
	var buf [16384]byte
	for {
		n, err := data.Read(buf[:])
		if err == io.EOF || (err == nil && n == 0) {
			break
		}
		if err != nil {
			return err
		}
		if err := stream.Send(&pb.InstallSnapshotRequest{
			Data: buf[:n],
		}); err != nil {
			return err
		}
	}
	ret, err := stream.CloseAndRecv()
	if err != nil {
		return err
	}
	resp = codec.DecodeInstallSnapshotResponse(ret)
	return nil
}

func (r raftApi) EncodePeer(id raft.ServerID, addr raft.ServerAddress) []byte {
	return []byte(addr)
}

func (r raftApi) DecodePeer(p []byte) raft.ServerAddress {
	return raft.ServerAddress(p)
}

func (r raftApi) SetHeartbeatHandler(cb func(rpc raft.RPC)) {
	r.m.heartbeatFuncMtx.Lock()
	r.m.heartbeatFunc = cb
	r.m.heartbeatFuncMtx.Unlock()
}

func (r raftApi) TimeoutNow(id raft.ServerID, target raft.ServerAddress, args *raft.TimeoutNowRequest, resp *raft.TimeoutNowResponse) error {
	ctx := context.Background()
	c, err := r.getPeer(id, target)
	if err != nil {
		return err
	}
	ret, err := c.TimeoutNow(ctx, codec.EncodeTimeoutNowRequest(args))
	if err != nil {
		return err
	}
	resp = codec.DecodeTimeoutNowResponse(ret)
	return nil
}

func (r raftApi) getPeer(id raft.ServerID, target raft.ServerAddress) (pb.RaftClient, error) {
	r.m.connectionMtx.Lock()
	c, ok := r.m.connections[id]
	if !ok {
		c := &conn{}
		r.m.connections[id] = c
	}
	r.m.connectionMtx.Unlock()

	c.mtx.Lock()
	defer c.mtx.Unlock()

	if c.clientConn == nil {
		cc, err := grpc.Dial(string(target), r.m.dialOptions...)
		if err != nil {
			return nil, err
		}
		c.clientConn = cc
		c.client = pb.NewRaftClient(cc)
	}
	return c.client, nil
}
