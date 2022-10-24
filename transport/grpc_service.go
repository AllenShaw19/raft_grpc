package transport

import (
	"context"
	pb "github.com/AllenShaw19/raft_grpc/proto"
)

type rpcService struct {
	manager *Manager
}

func (r rpcService) AppendEntriesPipeline(server pb.Raft_AppendEntriesPipelineServer) error {
	//TODO implement me
	panic("implement me")
}

func (r rpcService) AppendEntries(ctx context.Context, request *pb.AppendEntriesRequest) (*pb.AppendEntriesResponse, error) {
	//TODO implement me
	panic("implement me")
}

func (r rpcService) RequestVote(ctx context.Context, request *pb.RequestVoteRequest) (*pb.RequestVoteResponse, error) {
	//TODO implement me
	panic("implement me")
}

func (r rpcService) TimeoutNow(ctx context.Context, request *pb.TimeoutNowRequest) (*pb.TimeoutNowResponse, error) {
	//TODO implement me
	panic("implement me")
}

func (r rpcService) InstallSnapshot(server pb.Raft_InstallSnapshotServer) error {
	//TODO implement me
	panic("implement me")
}
