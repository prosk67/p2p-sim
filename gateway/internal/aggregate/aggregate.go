// Package aggregate builds the cluster view: each node's health and latency
// as probed by the gateway, plus that node's own view of its peers.
package aggregate

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"time"

	"github.com/p2p-sim/gateway/internal/config"
	"github.com/p2p-sim/gateway/internal/nodeclient"
)

type Node struct {
	ID         string          `json:"id"`
	URL        string          `json:"url"`
	Healthy    bool            `json:"healthy"`
	ObserverID *string         `json:"observer_id"`
	LatencyMS  *float64        `json:"latency_ms"`
	PeersSeen  map[string]bool `json:"peers_seen"`
	// Resources is the node's GET /resources body, or null when the node did
	// not provide one (an older node, or the call failed).
	Resources json.RawMessage `json:"resources"`
	Error     string          `json:"error,omitempty"`
}

type Snapshot struct {
	Nodes     []Node    `json:"nodes"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Cluster probes every peer concurrently (at most `concurrency` at once).
// Per-node failures are reported in that node's row.
func Cluster(ctx context.Context, client *nodeclient.Client, peers []config.Peer, concurrency int) Snapshot {
	if concurrency < 1 {
		concurrency = 1
	}
	nodes := make([]Node, len(peers))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, peer := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			nodes[i] = probe(ctx, client, peer)
		}()
	}
	wg.Wait()
	return Snapshot{Nodes: nodes, UpdatedAt: time.Now().UTC()}
}

func probe(ctx context.Context, client *nodeclient.Client, peer config.Peer) (node Node) {
	node = Node{ID: peer.ID, URL: peer.URL, PeersSeen: map[string]bool{}}
	start := time.Now()
	_, err := client.Health(ctx, peer.URL)
	if err != nil {
		node.Error = err.Error()
		return node
	}
	latency := math.Round(float64(time.Since(start).Microseconds())/100) / 10
	node.Healthy = true
	node.LatencyMS = &latency

	// /peers makes the node probe every peer, so fetch usage alongside it.
	usage := make(chan json.RawMessage, 1)
	go func() {
		body, _ := client.Resources(ctx, peer.URL)
		usage <- body
	}()
	defer func() { node.Resources = <-usage }()

	view, err := client.Peers(ctx, peer.URL)
	if err != nil {
		node.Error = "peer view unavailable: " + err.Error()
		return node
	}
	observer := view.NodeID
	node.ObserverID = &observer
	for _, seen := range view.Peers {
		node.PeersSeen[seen.ID] = seen.Healthy
	}
	return node
}
