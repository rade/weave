package router

import (
	"errors"
	"fmt"
	wt "github.com/zettio/weave/testing"
	"testing"
	"time"
)

type mockChannelConnection struct {
	mockConnection
	dest *Router
}

func (conn *mockChannelConnection) SendTCP(msg []byte) {
	channelHash, payload := decodeGossipChannel(msg[1:])
	if channel, found := conn.dest.GossipChannels[channelHash]; !found {
		panic(errors.New(fmt.Sprintf("unknown channel: %d", channelHash)))
	} else {
		srcName, payload := decodePeerName(payload)
		deliverGossip(channel, srcName, msg, payload)
	}
}

func (r1 *Router) AddTestChannelConnection(r2 *Router) {
	toName := r2.Ourself.Peer.Name
	toPeer := NewPeer(toName, r2.Ourself.Peer.UID, 0)
	r1.Peers.FetchWithDefault(toPeer) // Has side-effect of incrementing refcount
	r1.Ourself.Peer.connections[toName] = &mockChannelConnection{mockConnection{toPeer, ""}, r2}
	r1.Ourself.Peer.version += 1
	r1.Ourself.broadcastPeerUpdate(toPeer)
}

// Create a Peer object based on the name and UID of existing routers
func tp(r *Router, routers ...*Router) *Peer {
	peer := NewPeer(r.Ourself.Peer.Name, r.Ourself.Peer.UID, r.Ourself.Peer.version)
	for _, r2 := range routers {
		p2 := NewPeer(r2.Ourself.Peer.Name, r2.Ourself.Peer.UID, r2.Ourself.Peer.version)
		peer.connections[r2.Ourself.Peer.Name] = &mockConnection{p2, ""}
	}
	return peer
}

func checkEqualConns(t *testing.T, ourName PeerName, got, wanted map[PeerName]Connection) {
	checkConns := make(map[PeerName]bool)
	for _, conn := range wanted {
		checkConns[conn.Remote().Name] = true
	}
	for _, conn := range got {
		remoteName := conn.Remote().Name
		if _, found := checkConns[remoteName]; found {
			delete(checkConns, remoteName)
		} else {
			wt.Fatalf(t, "Unexpected connection from %s to %s", ourName, remoteName)
		}
	}
	if len(checkConns) > 0 {
		t.Fatalf("Expected connections not found: from %s to %v\n%s", ourName, checkConns, wt.StackTrace())
	}
}

func checkTopology(t *testing.T, router *Router, wantedPeers ...*Peer) {
	check := make(map[PeerName]*Peer)
	for _, peer := range wantedPeers {
		check[peer.Name] = peer
	}
	for _, peer := range router.Peers.table {
		name := peer.Name
		if wantedPeer, found := check[name]; found {
			checkEqualConns(t, name, peer.connections, wantedPeer.connections)
			delete(check, name)
		} else {
			t.Fatalf("Unexpected peer: %s\n%s", name, wt.StackTrace())
		}
	}
	if len(check) > 0 {
		t.Fatalf("Expected peers not found: %v\n%s", check, wt.StackTrace())
	}
}

func TestGossipTopology(t *testing.T) {
	wt.RunWithTimeout(t, 1*time.Second, func() {
		implTestGossipTopology(t)
	})
}

func implTestGossipTopology(t *testing.T) {
	const (
		peer1NameString = "01:00:00:01:00:00"
		peer2NameString = "02:00:00:02:00:00"
		peer3NameString = "03:00:00:03:00:00"
	)
	var (
		peer1Name, _ = PeerNameFromString(peer1NameString)
		peer2Name, _ = PeerNameFromString(peer2NameString)
		peer3Name, _ = PeerNameFromString(peer3NameString)
	)

	removed := &peerQueue{nil}

	// Create some peers that will talk to each other
	r1 := NewTestRouter(t, peer1Name, removed)
	r2 := NewTestRouter(t, peer2Name, removed)
	r3 := NewTestRouter(t, peer3Name, removed)
	r1.NewGossip(TopologyGossipCh, r1)
	r2.NewGossip(TopologyGossipCh, r2)
	r3.NewGossip(TopologyGossipCh, r3)

	AssertEmpty(t, removed.peers, "garbage-collected peers")

	// Check state when they have no connections
	checkTopology(t, r1, tp(r1))
	checkTopology(t, r2, tp(r2))

	// Now try adding some connections
	r1.AddTestChannelConnection(r2)
	checkTopology(t, r1, tp(r1, r2), tp(r2))
	checkTopology(t, r2, tp(r2))
	r2.AddTestChannelConnection(r1)
	checkTopology(t, r1, tp(r1, r2), tp(r2, r1))
	checkTopology(t, r2, tp(r2, r1), tp(r1, r2))

	// Currently, the connection from 2 to 3 is one-way only
	r2.AddTestChannelConnection(r3)
	checkTopology(t, r1, tp(r1, r2), tp(r2, r1, r3), tp(r3))
	checkTopology(t, r2, tp(r1, r2), tp(r2, r1, r3), tp(r3))
	checkTopology(t, r3, tp(r3))
	// When r2 gossiped to r3, 1 and 2 were unreachable from r3 so they got removed from the update
	checkPeerArray(t, removed.peers, rs(r1, r2))
	removed.clear()

	// Add a connection from 3 to 1 and now r1 is reachable.
	r3.AddTestChannelConnection(r1)
	checkTopology(t, r1, tp(r1, r2), tp(r2, r1, r3), tp(r3, r1))
	checkTopology(t, r2, tp(r1, r2), tp(r2, r1, r3), tp(r3, r1))
	checkTopology(t, r3, tp(r1), tp(r3, r1))
	AssertEmpty(t, removed.peers, "garbage-collected peers")

	r1.AddTestChannelConnection(r3)
	checkTopology(t, r1, tp(r1, r2, r3), tp(r2, r1, r3), tp(r3, r1))
	checkTopology(t, r2, tp(r1, r2, r3), tp(r2, r1, r3), tp(r3, r1))
	checkTopology(t, r3, tp(r1, r2, r3), tp(r2, r1, r3), tp(r3, r1))
	AssertEmpty(t, removed.peers, "garbage-collected peers")

	// Drop the connection from 2 to 3
	r2.DeleteTestConnection(r3)
	checkTopology(t, r2, tp(r1, r2, r3), tp(r2, r1), tp(r3, r1))

	// Now r2 tells its connections
	r2.Ourself.broadcastPeerUpdate()
	checkTopology(t, r1, tp(r1, r2, r3), tp(r2, r1), tp(r3, r1))
	checkTopology(t, r2, tp(r1, r2, r3), tp(r2, r1), tp(r3, r1))
	checkTopology(t, r3, tp(r1, r2, r3), tp(r2, r1), tp(r3, r1))

	// Drop the connection from 1 to 3, and it will get removed by garbage-collection
	r1.DeleteTestConnection(r3)
	checkTopology(t, r1, tp(r1, r2), tp(r2, r1), tp(r3, r1))
	r1.Peers.GarbageCollect()
	checkTopology(t, r1, tp(r1, r2), tp(r2, r1))
	removed.clear()

	// Now r1 tells its remaining connection, which also garbage-collects 3
	r1.Ourself.broadcastPeerUpdate()
	checkPeerArray(t, removed.peers, rs(r3))
	removed.clear()

	checkTopology(t, r1, tp(r1, r2), tp(r2, r1))
	checkTopology(t, r2, tp(r1, r2), tp(r2, r1))
	// r3 still thinks r1 has a connection to it
	checkTopology(t, r3, tp(r1, r2, r3), tp(r2, r1), tp(r3, r1))

	// On a timer, r3 will gossip to r1
	r3.SendAllGossip()
	// r1 receives info about 3, but eliminates it through garbage collection
	checkTopology(t, r1, tp(r1, r2), tp(r2, r1))
	checkPeerArray(t, removed.peers, rs(r3))
	removed.clear()
}
