package router

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"log"
	"sync"
	"time"
)

const GossipInterval = 30 * time.Second

type GossipData interface {
	Encode() []byte
	Merge(GossipData)
}

type Gossip interface {
	// specific message from one peer to another
	// intermediate peers relay it using unicast topology.
	GossipUnicast(dstPeerName PeerName, buf []byte) error
	// send a message to every peer, relayed using broadcast topology.
	GossipBroadcast(buf []byte) error
}

type Gossiper interface {
	OnGossipUnicast(sender PeerName, msg []byte) error
	OnGossipBroadcast(msg []byte) error
	// return state of everything we know; gets called periodically
	Gossip() GossipData
	// merge in state and return "everything new I've just learnt",
	// or nil if nothing in the received message was new
	OnGossip(buf []byte) (GossipData, error)
}

// Accumulates GossipData that needs to be sent to one destination,
// and sends it when possible.
type GossipSender struct {
	sync.Mutex
	channel  *GossipChannel
	sender   ProtocolSender
	pending  GossipData
	sendChan chan<- bool
}

func NewGossipSender(c *GossipChannel, ps ProtocolSender) *GossipSender {
	return &GossipSender{channel: c, sender: ps}
}

func (sender *GossipSender) Start() {
	sendChan := make(chan bool, 1)
	sender.sendChan = sendChan
	go sender.run(sendChan)
}

func (sender *GossipSender) run(sendingChan <-chan bool) {
	for {
		if val := <-sendingChan; !val { // receive zero value when chan is closed
			break
		}
		sender.sendPending()
	}
}

func (sender *GossipSender) sendPending() bool {
	sender.Lock()
	pending := sender.pending
	sender.pending = nil
	sender.Unlock() // don't hold the lock while calling Encode which may take other locks
	if pending == nil {
		return false
	}
	sender.sender.SendProtocolMsg(sender.channel.gossipMsg(pending.Encode()))
	return true
}

func (sender *GossipSender) Send(data GossipData) {
	sender.Lock()
	if sender.pending == nil {
		sender.pending = data
	} else {
		sender.pending.Merge(data)
	}
	sender.Unlock()
	select { // non-blocking send
	case sender.sendChan <- true:
	default:
	}
}

func (sender *GossipSender) Stop() {
	close(sender.sendChan)
}

type senderMap map[Connection]*GossipSender

type GossipChannel struct {
	sync.Mutex
	ourself  *LocalPeer
	name     string
	hash     uint32
	gossiper Gossiper
	senders  senderMap
}

func (router *Router) NewGossip(channelName string, g Gossiper) Gossip {
	channelHash := hash(channelName)
	channel := &GossipChannel{
		ourself:  router.Ourself,
		name:     channelName,
		hash:     channelHash,
		gossiper: g,
		senders:  make(senderMap)}
	router.GossipChannels[channelHash] = channel
	return channel
}

func (router *Router) SendAllGossip() {
	for _, channel := range router.GossipChannels {
		channel.SendGossip(channel.gossiper.Gossip())
	}
}

func (router *Router) SendAllGossipDown(conn Connection) {
	for _, channel := range router.GossipChannels {
		channel.SendGossipDown(conn, channel.gossiper.Gossip())
	}
}

func (router *Router) handleGossip(payload []byte, onok func(*GossipChannel, PeerName, []byte, *gob.Decoder) error) error {
	decoder := gob.NewDecoder(bytes.NewReader(payload))
	var channelHash uint32
	if err := decoder.Decode(&channelHash); err != nil {
		return err
	}
	channel, found := router.GossipChannels[channelHash]
	if !found {
		return fmt.Errorf("[gossip] received unknown channel with hash %v", channelHash)
	}
	var srcName PeerName
	if err := decoder.Decode(&srcName); err != nil {
		return err
	}
	if err := onok(channel, srcName, payload, decoder); err != nil {
		return err
	}
	return nil
}

func deliverGossipUnicast(channel *GossipChannel, srcName PeerName, origPayload []byte, dec *gob.Decoder) error {
	var destName PeerName
	if err := dec.Decode(&destName); err != nil {
		return err
	}
	if channel.ourself.Name == destName {
		var payload []byte
		if err := dec.Decode(&payload); err != nil {
			return err
		}
		return channel.gossiper.OnGossipUnicast(srcName, payload)
	} else {
		return channel.relayGossipUnicast(destName, origPayload)
	}
}

func deliverGossipBroadcast(channel *GossipChannel, srcName PeerName, origPayload []byte, dec *gob.Decoder) error {
	var payload []byte
	if err := dec.Decode(&payload); err != nil {
		return err
	}
	if err := channel.gossiper.OnGossipBroadcast(payload); err != nil {
		return err
	}
	return channel.relayGossipBroadcast(srcName, origPayload)
}

func deliverGossip(channel *GossipChannel, srcName PeerName, _ []byte, dec *gob.Decoder) error {
	var payload []byte
	if err := dec.Decode(&payload); err != nil {
		return err
	}
	if data, err := channel.gossiper.OnGossip(payload); err != nil {
		return err
	} else if data != nil {
		channel.SendGossip(data)
	}
	return nil
}

func (c *GossipChannel) SendGossip(data GossipData) {
	connections := c.ourself.Connections() // do this outside the lock so they don't nest
	c.Lock()
	for _, conn := range connections {
		c.sendGossipDown(conn, data)
	}
	c.Unlock()
	c.garbageCollectSenders() // TODO merge into the above
}

func (c *GossipChannel) SendGossipDown(conn Connection, data GossipData) {
	c.Lock()
	c.sendGossipDown(conn, data)
	c.Unlock()
}

func (c *GossipChannel) sendGossipDown(conn Connection, data GossipData) {
	sender, found := c.senders[conn]
	if !found {
		sender = NewGossipSender(c, conn.(ProtocolSender))
		c.senders[conn] = sender
		sender.Start()
	}
	// our callers hold a lock on c; we lock sender
	sender.Send(data)
}

// Copy senders corresponding to current connections, then close down
// any remaining senders.
func (c *GossipChannel) garbageCollectSenders() {
	connections := c.ourself.Connections() // do this outside the lock so they don't nest
	newSenders := make(senderMap)
	c.Lock()
	defer c.Unlock()
	for _, conn := range connections {
		newSenders[conn] = c.senders[conn]
		delete(c.senders, conn)
	}
	for _, sender := range c.senders {
		sender.Stop()
	}
	c.senders = newSenders
}

func (c *GossipChannel) gossipMsg(buf []byte) ProtocolMsg {
	return ProtocolMsg{ProtocolGossip, GobEncode(c.hash, c.ourself.Name, buf)}
}

func (c *GossipChannel) GossipUnicast(dstPeerName PeerName, buf []byte) error {
	return c.relayGossipUnicast(dstPeerName, GobEncode(c.hash, c.ourself.Name, dstPeerName, buf))
}

func (c *GossipChannel) GossipBroadcast(buf []byte) error {
	return c.relayGossipBroadcast(c.ourself.Name, GobEncode(c.hash, c.ourself.Name, buf))
}

func (c *GossipChannel) relayGossipUnicast(dstPeerName PeerName, msg []byte) error {
	if relayPeerName, found := c.ourself.Router.Routes.Unicast(dstPeerName); !found {
		c.log("unknown relay destination:", dstPeerName)
	} else if conn, found := c.ourself.ConnectionTo(relayPeerName); !found {
		c.log("unable to find connection to relay peer", relayPeerName)
	} else {
		conn.(ProtocolSender).SendProtocolMsg(ProtocolMsg{ProtocolGossipUnicast, msg})
	}
	return nil
}

func (c *GossipChannel) relayGossipBroadcast(srcName PeerName, msg []byte) error {
	if srcPeer, found := c.ourself.Router.Peers.Fetch(srcName); !found {
		c.log("unable to relay broadcast from unknown peer", srcName)
	} else {
		protocolMsg := ProtocolMsg{ProtocolGossipBroadcast, msg}
		for _, conn := range c.ourself.NextBroadcastHops(srcPeer) {
			conn.SendProtocolMsg(protocolMsg)
		}
	}
	return nil
}

func (c *GossipChannel) log(args ...interface{}) {
	log.Println(append(append([]interface{}{}, "[gossip "+c.name+"]:"), args...)...)
}
