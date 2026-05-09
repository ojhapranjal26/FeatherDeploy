package coordination

import (
	"context"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

type Client struct {
	etcd *clientv3.Client
}

func NewClient(endpoints []string) (*Client, error) {
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return &Client{etcd: cli}, nil
}

func (c *Client) Close() error {
	return c.etcd.Close()
}

// RegisterNode heartbeats a node's presence using an etcd lease.
// The lease will expire if the node stops heartbeating, allowing the cluster
// to detect node failure in real-time.
func (c *Client) RegisterNode(ctx context.Context, nodeID string, ttl int64) (<-chan *clientv3.LeaseKeepAliveResponse, error) {
	lease, err := c.etcd.Grant(ctx, ttl)
	if err != nil {
		return nil, err
	}

	key := fmt.Sprintf("/nodes/heartbeat/%s", nodeID)
	_, err = c.etcd.Put(ctx, key, "alive", clientv3.WithLease(lease.ID))
	if err != nil {
		return nil, err
	}

	return c.etcd.KeepAlive(ctx, lease.ID)
}

// WatchNodes returns a channel that emits events when nodes join or leave.
func (c *Client) WatchNodes(ctx context.Context) clientv3.WatchChan {
	return c.etcd.Watch(ctx, "/nodes/heartbeat/", clientv3.WithPrefix())
}

// ElectLeader attempts to claim the leader role (brain) using etcd election.
func (c *Client) ElectLeader(ctx context.Context, electionName, candidateID string) (chan bool, error) {
	session, err := concurrency.NewSession(c.etcd)
	if err != nil {
		return nil, err
	}

	election := concurrency.NewElection(session, "/elections/"+electionName)
	leaderChan := make(chan bool, 1)

	go func() {
		defer session.Close()
		if err := election.Campaign(ctx, candidateID); err != nil {
			leaderChan <- false
			return
		}
		leaderChan <- true
		// Stay leader until context is canceled
		<-ctx.Done()
		election.Resign(context.Background())
	}()

	return leaderChan, nil
}
