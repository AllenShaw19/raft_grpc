package transport

import (
	"context"
	pb "github.com/AllenShaw19/raft_grpc/proto"
	"github.com/AllenShaw19/raft_grpc/transport/codec"
	"github.com/hashicorp/raft"
	"io"
)

type snapshotStream struct {
	s   pb.Raft_InstallSnapshotServer
	buf []byte
}

func (s *snapshotStream) Read(b []byte) (int, error) {
	if len(s.buf) > 0 {
		n := copy(b, s.buf)
		s.buf = s.buf[n:]
		return n, nil
	}
	m, err := s.s.Recv()
	if err != nil {
		return 0, err
	}
	n := copy(b, m.GetData())
	if n < len(m.GetData()) {
		s.buf = m.GetData()[n:]
	}
	return n, nil
}

type rpcService struct {
	manager *Manager
}

func (s rpcService) handleRPC(command interface{}, data io.Reader) (interface{}, error) {
	ch := make(chan raft.RPCResponse, 1)
	rpc := raft.RPC{
		Command:  command,
		RespChan: ch,
		Reader:   data,
	}
	if isHeartbeat(command) {
		// We can take the fast path and use the heartbeat callback and skip the queue in s.manager.rpcChan.
		s.manager.heartbeatFuncMtx.Lock()
		fn := s.manager.heartbeatFunc
		s.manager.heartbeatFuncMtx.Unlock()
		if fn != nil {
			fn(rpc)
			goto wait
		}
	}
	s.manager.rpcChan <- rpc
wait:
	resp := <-ch
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Response, nil
}

func (s rpcService) AppendEntriesPipeline(server pb.Raft_AppendEntriesPipelineServer) error {
	for {
		msg, err := server.Recv()
		if err != nil {
			return err
		}
		resp, err := s.handleRPC(codec.DecodeAppendEntriesRequest(msg), nil)
		if err != nil {
			// TODO(quis): One failure doesn't have to break the entire stream?
			// Or does it all go wrong when it's out of order anyway?
			return err
		}
		if err := server.Send(codec.EncodeAppendEntriesResponse(resp.(*raft.AppendEntriesResponse))); err != nil {
			return err
		}
	}
}

func (s rpcService) AppendEntries(ctx context.Context, req *pb.AppendEntriesRequest) (*pb.AppendEntriesResponse, error) {
	resp, err := s.handleRPC(codec.DecodeAppendEntriesRequest(req), nil)
	if err != nil {
		return nil, err
	}
	return codec.EncodeAppendEntriesResponse(resp.(*raft.AppendEntriesResponse)), nil
}

func (s rpcService) RequestVote(ctx context.Context, req *pb.RequestVoteRequest) (*pb.RequestVoteResponse, error) {
	resp, err := s.handleRPC(codec.DecodeRequestVoteRequest(req), nil)
	if err != nil {
		return nil, err
	}
	return codec.EncodeRequestVoteResponse(resp.(*raft.RequestVoteResponse)), nil
}

func (s rpcService) TimeoutNow(ctx context.Context, req *pb.TimeoutNowRequest) (*pb.TimeoutNowResponse, error) {
	resp, err := s.handleRPC(codec.DecodeTimeoutNowRequest(req), nil)
	if err != nil {
		return nil, err
	}
	return codec.EncodeTimeoutNowResponse(resp.(*raft.TimeoutNowResponse)), nil
}

func (s rpcService) InstallSnapshot(server pb.Raft_InstallSnapshotServer) error {
	isr, err := server.Recv()
	if err != nil {
		return err
	}
	resp, err := s.handleRPC(codec.DecodeInstallSnapshotRequest(isr), &snapshotStream{server, isr.GetData()})
	if err != nil {
		return err
	}
	return server.SendAndClose(codec.EncodeInstallSnapshotResponse(resp.(*raft.InstallSnapshotResponse)))
}

func isHeartbeat(command interface{}) bool {
	req, ok := command.(*raft.AppendEntriesRequest)
	if !ok {
		return false
	}
	return req.Term != 0 && len(req.Leader) != 0 && req.PrevLogEntry == 0 && req.PrevLogTerm == 0 && len(req.Entries) == 0 && req.LeaderCommitIndex == 0
}
