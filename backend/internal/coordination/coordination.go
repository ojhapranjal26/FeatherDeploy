package coordination

import (
	"context"
	"encoding/json"
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
		// Memory optimization: Reduce gRPC window sizes and use keepalives
		// to prevent stale connections from consuming memory.
		MaxCallSendMsgSize: 2 * 1024 * 1024, // 2MB
		MaxCallRecvMsgSize: 2 * 1024 * 1024, // 2MB
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
func (c *Client) RegisterNode(ctx context.Context, nodeID, ip string, port int, ttl int64) (<-chan *clientv3.LeaseKeepAliveResponse, error) {
	lease, err := c.etcd.Grant(ctx, ttl)
	if err != nil {
		return nil, err
	}

	key := fmt.Sprintf("/nodes/heartbeat/%s", nodeID)
	val := fmt.Sprintf(`{"ip":"%s","port":%d}`, ip, port)
	_, err = c.etcd.Put(ctx, key, val, clientv3.WithLease(lease.ID))
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
// RegisterService registers a service endpoint in etcd.
func (c *Client) RegisterService(ctx context.Context, projectID int64, svcName, nodeIP string, port int) error {
	key := fmt.Sprintf("/discovery/services/%d/%s", projectID, svcName)
	val := fmt.Sprintf(`{"ip":"%s","port":%d,"type":"service"}`, nodeIP, port)
	_, err := c.etcd.Put(ctx, key, val)
	return err
}

func (c *Client) UnregisterService(ctx context.Context, projectID int64, svcName string) error {
	key := fmt.Sprintf("/discovery/services/%d/%s", projectID, svcName)
	_, err := c.etcd.Delete(ctx, key)
	return err
}

// RegisterDatabase registers a database endpoint in etcd.
func (c *Client) RegisterDatabase(ctx context.Context, projectID int64, dbName, nodeIP string, port int) error {
	key := fmt.Sprintf("/discovery/databases/%d/%s", projectID, dbName)
	val := fmt.Sprintf(`{"ip":"%s","port":%d,"type":"database"}`, nodeIP, port)
	_, err := c.etcd.Put(ctx, key, val)
	return err
}

func (c *Client) UnregisterDatabase(ctx context.Context, projectID int64, name string) error {
	key := fmt.Sprintf("/discovery/databases/%d/%s", projectID, name)
	_, err := c.etcd.Delete(ctx, key)
	return err
}

// DiscoverDatabase fetches database metadata from etcd.
func (c *Client) DiscoverDatabase(ctx context.Context, projectID int64, name string) (string, int, error) {
	key := fmt.Sprintf("/discovery/databases/%d/%s", projectID, name)
	resp, err := c.etcd.Get(ctx, key)
	if err != nil {
		return "", 0, err
	}
	if len(resp.Kvs) == 0 {
		return "", 0, fmt.Errorf("database not found")
	}
	var data struct {
		IP   string `json:"ip"`
		Port int    `json:"port"`
	}
	if err := json.Unmarshal(resp.Kvs[0].Value, &data); err != nil {
		return "", 0, err
	}
	return data.IP, data.Port, nil
}

// RegisterStorage registers an object storage endpoint in etcd.
func (c *Client) RegisterStorage(ctx context.Context, projectID int64, storageName, nodeIP string) error {
	key := fmt.Sprintf("/discovery/storage/%d/%s", projectID, storageName)
	val := fmt.Sprintf(`{"ip":"%s","type":"storage"}`, nodeIP)
	_, err := c.etcd.Put(ctx, key, val)
	return err
}

// DiscoverService fetches service metadata from etcd.
func (c *Client) DiscoverService(ctx context.Context, projectID int64, svcName string) (string, int, error) {
	key := fmt.Sprintf("/discovery/services/%d/%s", projectID, svcName)
	resp, err := c.etcd.Get(ctx, key)
	if err != nil {
		return "", 0, err
	}
	if len(resp.Kvs) == 0 {
		return "", 0, fmt.Errorf("service not found")
	}
	var data struct {
		IP   string `json:"ip"`
		Port int    `json:"port"`
	}
	if err := json.Unmarshal(resp.Kvs[0].Value, &data); err != nil {
		return "", 0, err
	}
	return data.IP, data.Port, nil
}

// Put writes a key-value pair to Etcd.
func (c *Client) Put(ctx context.Context, key, val string) error {
	_, err := c.etcd.Put(ctx, key, val)
	return err
}

// Delete removes a key from Etcd.
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.etcd.Delete(ctx, key)
	return err
}

// GetPrefix gets all keys with the given prefix.
func (c *Client) GetPrefix(ctx context.Context, prefix string) (*clientv3.GetResponse, error) {
	return c.etcd.Get(ctx, prefix, clientv3.WithPrefix())
}

// EtcdClient gives direct access to the underlying clientv3.Client.
func (c *Client) EtcdClient() *clientv3.Client {
	return c.etcd
}
