// Copyright 2019 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package execinfrapb

import (
	"context"
	"net"
	"time"

	"github.com/cockroachdb/cockroach/pkg/base"
	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/rpc"
	"github.com/cockroachdb/cockroach/pkg/settings/cluster"
	"github.com/cockroachdb/cockroach/pkg/util"
	"github.com/cockroachdb/cockroach/pkg/util/hlc"
	"github.com/cockroachdb/cockroach/pkg/util/netutil"
	"github.com/cockroachdb/cockroach/pkg/util/stop"
	"github.com/cockroachdb/cockroach/pkg/util/syncutil"
	"github.com/cockroachdb/cockroach/pkg/util/uuid"
	"github.com/cockroachdb/logtags"
	"google.golang.org/grpc"
)

func newInsecureRPCContext(ctx context.Context, stopper *stop.Stopper) *rpc.Context {
	nc := &base.NodeIDContainer{}
	ctx = logtags.AddTag(ctx, "n", nc)
	return rpc.NewContext(ctx,
		rpc.ContextOptions{
			TenantID: roachpb.SystemTenantID,
			NodeID:   nc,
			Config:   &base.Config{Insecure: true},
			Clock:    hlc.NewClock(hlc.UnixNano, time.Nanosecond),
			Stopper:  stopper,
			Settings: cluster.MakeTestingClusterSettings(),
		})
}

// StartMockDistSQLServer starts a MockDistSQLServer and returns the address on
// which it's listening.
func StartMockDistSQLServer(
	ctx context.Context, clock *hlc.Clock, stopper *stop.Stopper, nodeID roachpb.NodeID,
) (uuid.UUID, *MockDistSQLServer, net.Addr, error) {
	rpcContext := newInsecureRPCContext(ctx, stopper)
	rpcContext.NodeID.Set(context.TODO(), nodeID)
	server := rpc.NewServer(rpcContext)
	mock := newMockDistSQLServer()
	RegisterDistSQLServer(server, mock)
	ln, err := netutil.ListenAndServeGRPC(stopper, server, util.IsolatedTestAddr)
	if err != nil {
		return uuid.Nil, nil, nil, err
	}
	return rpcContext.ClusterID.Get(), mock, ln.Addr(), nil
}

// MockDistSQLServer implements the DistSQLServer (gRPC) interface and allows
// clients to control the inbound streams.
type MockDistSQLServer struct {
	InboundStreams chan InboundStreamNotification
}

// InboundStreamNotification is the MockDistSQLServer's way to tell its clients
// that a new gRPC call has arrived and thus a stream has arrived. The rpc
// handler is blocked until Donec is signaled.
type InboundStreamNotification struct {
	Stream DistSQL_FlowStreamServer
	Donec  chan<- error
}

// MockDistSQLServer implements the DistSQLServer interface.
var _ DistSQLServer = &MockDistSQLServer{}

func newMockDistSQLServer() *MockDistSQLServer {
	return &MockDistSQLServer{
		InboundStreams: make(chan InboundStreamNotification),
	}
}

// SetupFlow is part of the DistSQLServer interface.
func (ds *MockDistSQLServer) SetupFlow(
	_ context.Context, req *SetupFlowRequest,
) (*SimpleResponse, error) {
	return nil, nil
}

// CancelDeadFlows is part of the DistSQLServer interface.
func (ds *MockDistSQLServer) CancelDeadFlows(
	_ context.Context, req *CancelDeadFlowsRequest,
) (*SimpleResponse, error) {
	return nil, nil
}

// FlowStream is part of the DistSQLServer interface.
func (ds *MockDistSQLServer) FlowStream(stream DistSQL_FlowStreamServer) error {
	donec := make(chan error)
	ds.InboundStreams <- InboundStreamNotification{Stream: stream, Donec: donec}
	return <-donec
}

// MockDialer is a mocked implementation of the Outbox's `Dialer` interface.
// Used to create a connection with a client stream.
type MockDialer struct {
	// Addr is assumed to be obtained from execinfrapb.StartMockDistSQLServer.
	Addr net.Addr
	mu   struct {
		syncutil.Mutex
		conn *grpc.ClientConn
	}
}

// DialNoBreaker establishes a grpc connection once.
func (d *MockDialer) DialNoBreaker(
	context.Context, roachpb.NodeID, rpc.ConnectionClass,
) (*grpc.ClientConn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.mu.conn != nil {
		return d.mu.conn, nil
	}
	var err error
	//lint:ignore SA1019 grpc.WithInsecure is deprecated
	d.mu.conn, err = grpc.Dial(d.Addr.String(), grpc.WithInsecure(), grpc.WithBlock())
	return d.mu.conn, err
}

// Close must be called after the test is done.
func (d *MockDialer) Close() {
	err := d.mu.conn.Close() // nolint:grpcconnclose
	if err != nil {
		panic(err)
	}
}
