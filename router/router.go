// Package router provides a router for murcott.
package router

import (
	"bytes"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/h2so5/murcott/dht"
	"github.com/h2so5/murcott/internal"
	"github.com/h2so5/murcott/log"
	"github.com/h2so5/murcott/utils"
	"github.com/h2so5/utp"
)

type Message struct {
	Node    utils.NodeID
	Payload []byte
	ID      []byte
}

type Router struct {
	id       utils.NodeID
	mainDht  *dht.DHT
	groupDht map[string]*dht.DHT
	dhtMutex sync.RWMutex

	listener *utp.Listener
	key      *utils.PrivateKey

	sessions     map[string]*session
	sessionMutex sync.RWMutex

	queuedPackets []internal.Packet

	logger *log.Logger
	recv   chan Message
	send   chan internal.Packet
	exit   chan int
}

func getOpenPortConn(config utils.Config) (*utp.Listener, error) {
	for _, port := range config.Ports() {
		addr, err := utp.ResolveAddr("utp", ":"+strconv.Itoa(port))
		conn, err := utp.Listen("utp", addr)
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("fail to bind port")
}

func NewRouter(key *utils.PrivateKey, logger *log.Logger, config utils.Config) (*Router, error) {
	exit := make(chan int)
	listener, err := getOpenPortConn(config)
	if err != nil {
		return nil, err
	}

	logger.Info("Node ID: %s", key.Digest().String())
	logger.Info("Node Socket: %v", listener.Addr())

	ns := utils.GlobalNamespace
	id := utils.NewNodeID(ns, key.Digest())

	r := Router{
		id:       id,
		listener: listener,
		key:      key,
		sessions: make(map[string]*session),
		mainDht:  dht.NewDHT(10, id, id, listener.RawConn, logger),
		groupDht: make(map[string]*dht.DHT),

		logger: logger,
		recv:   make(chan Message, 100),
		send:   make(chan internal.Packet, 100),
		exit:   exit,
	}

	go r.run()
	return &r, nil
}

func (p *Router) Discover(addrs []net.UDPAddr) {
	p.dhtMutex.RLock()
	defer p.dhtMutex.RUnlock()
	for _, addr := range addrs {
		p.mainDht.Discover(&addr)
		for _, d := range p.groupDht {
			d.Discover(&addr)
		}
		p.logger.Info("Sent discovery packet to %v:%d", addr.IP, addr.Port)
	}
}

func (p *Router) Join(group utils.NodeID) error {
	p.dhtMutex.Lock()
	defer p.dhtMutex.Unlock()
	if _, ok := p.groupDht[group.String()]; !ok {
		p.groupDht[group.String()] = dht.NewDHT(10, p.ID(), group, p.listener.RawConn, p.logger)
		return nil
	}
	return errors.New("already joined")
}

func (p *Router) Leave(group utils.NodeID) error {
	p.dhtMutex.Lock()
	defer p.dhtMutex.Unlock()
	if _, ok := p.groupDht[group.String()]; ok {
		delete(p.groupDht, group.String())
		return nil
	}
	return errors.New("not joined")
}

func (p *Router) SendMessage(dst utils.NodeID, payload []byte) error {
	pkt, err := p.makePacket(dst, "msg", payload)
	if err != nil {
		return err
	}
	p.send <- pkt
	return nil
}

func (p *Router) SendPing() {
	var list []utils.NodeID

	p.sessionMutex.RLock()
	for str, _ := range p.sessions {
		id, _ := utils.NewNodeIDFromString(str)
		list = append(list, id)
	}
	p.sessionMutex.RUnlock()

	for _, id := range list {
		pkt, err := p.makePacket(id, "ping", nil)
		if err == nil {
			p.send <- pkt
		}
	}
}

func (p *Router) RecvMessage() (Message, error) {
	if m, ok := <-p.recv; ok {
		return m, nil
	}
	return Message{}, errors.New("Node closed")
}

func (p *Router) run() {
	acceptch := make(chan *session)

	go func() {
		for {
			conn, err := p.listener.Accept()
			if err != nil {
				p.logger.Error("%v", err)
				return
			}
			s, err := newSesion(conn, p.key)
			if err != nil {
				conn.Close()
				p.logger.Error("%v", err)
				continue
			} else {
				go p.readSession(s)
				acceptch <- s
			}
		}
	}()

	go func() {
		var b [102400]byte
		for {
			l, addr, err := p.listener.RawConn.ReadFrom(b[:])
			if err != nil {
				p.logger.Error("%v", err)
				return
			}
			p.dhtMutex.RLock()
			p.mainDht.ProcessPacket(b[:l], addr)
			for _, d := range p.groupDht {
				d.ProcessPacket(b[:l], addr)
			}
			p.dhtMutex.RUnlock()
		}
	}()

	for {
		select {
		case s := <-acceptch:
			p.addSession(s)
		case pkt := <-p.send:
			sessions := p.getSessions(pkt.Dst)
			if len(sessions) > 0 {
				for _, s := range sessions {
					err := s.Write(pkt)
					if err != nil {
						p.logger.Error("Remove session(%s): %v", pkt.Dst.String(), err)
						p.removeSession(s)
						p.queuedPackets = append(p.queuedPackets, pkt)
					}
				}
			} else {
				p.logger.Error("Route not found: %v", pkt.Dst)
				p.queuedPackets = append(p.queuedPackets, pkt)
			}
		case <-time.After(time.Second):
			p.SendPing()
			var rest []internal.Packet
			for _, pkt := range p.queuedPackets {
				p.dhtMutex.RLock()
				p.mainDht.FindNearestNode(pkt.Dst)
				for _, d := range p.groupDht {
					d.FindNearestNode(pkt.Dst)
				}
				p.dhtMutex.RUnlock()
				sessions := p.getSessions(pkt.Dst)
				if len(sessions) > 0 {
					for _, s := range sessions {
						err := s.Write(pkt)
						if err != nil {
							p.logger.Error("Remove session(%s): %v", pkt.Dst.String(), err)
							p.removeSession(s)
							p.queuedPackets = append(p.queuedPackets, pkt)
						}
					}
				} else {
					p.logger.Error("Route not found: %v", pkt.Dst)
					rest = append(rest, pkt)
				}
			}
			p.queuedPackets = rest

		case <-p.exit:
			return
		}
	}
}

func (p *Router) addSession(s *session) {
	p.sessionMutex.Lock()
	defer p.sessionMutex.Unlock()
	id := s.ID().String()
	if _, ok := p.sessions[id]; !ok {
		p.sessions[id] = s
	}
}

func (p *Router) removeSession(s *session) {
	p.sessionMutex.Lock()
	defer p.sessionMutex.Unlock()
	id := s.ID().String()
	delete(p.sessions, id)
}

func (p *Router) readSession(s *session) {
	for {
		pkt, err := s.Read()
		if err != nil {
			p.logger.Error("Remove session(%s): %v", pkt.Dst.String(), err)
			p.removeSession(s)
			return
		}
		ns := utils.GlobalNamespace
		if !bytes.Equal(pkt.Src.NS[:], ns[:]) {
			p.dhtMutex.RLock()
			if d, ok := p.groupDht[pkt.Dst.String()]; ok {
				pkt.TTL--
				if pkt.TTL > 0 {
					for _, n := range d.KnownNodes() {
						sessions := p.getSessions(n.ID)
						for _, s := range sessions {
							s.Write(pkt)
						}
					}
				}
			}
			p.dhtMutex.RUnlock()
		}
		if pkt.Type == "msg" {
			id, _ := time.Now().MarshalBinary()
			p.recv <- Message{Node: pkt.Src, Payload: pkt.Payload, ID: id}
		}
	}
}

func (p *Router) getSessions(id utils.NodeID) []*session {
	var sessions []*session
	if bytes.Equal(id.NS[:], utils.GlobalNamespace[:]) {
		s := p.getDirectSession(id)
		if s != nil {
			sessions = append(sessions, s)
		}
	} else {
		if d, ok := p.groupDht[id.String()]; ok {
			for _, n := range d.KnownNodes() {
				s := p.getDirectSession(n.ID)
				if s != nil {
					sessions = append(sessions, s)
				}
			}
		}
	}
	return sessions
}

func (p *Router) getDirectSession(id utils.NodeID) *session {
	idstr := id.String()
	p.sessionMutex.RLock()
	if s, ok := p.sessions[idstr]; ok {
		p.sessionMutex.RUnlock()
		return s
	}
	p.sessionMutex.RUnlock()

	var info *utils.NodeInfo
	p.dhtMutex.RLock()
	info = p.mainDht.GetNodeInfo(id)
	if info == nil {
		for _, d := range p.groupDht {
			info = d.GetNodeInfo(id)
			if info != nil {
				break
			}
		}
	}
	p.dhtMutex.RUnlock()

	if info == nil {
		return nil
	}

	addr, err := utp.ResolveAddr("utp", info.Addr.String())
	if err != nil {
		p.logger.Error("%v", err)
		return nil
	}

	conn, err := utp.DialUTP("utp", nil, addr)
	if err != nil {
		p.logger.Error("%v", err)
		return nil
	}

	s, err := newSesion(conn, p.key)
	if err != nil {
		conn.Close()
		p.logger.Error("%v", err)
		return nil
	} else {
		go p.readSession(s)
		p.addSession(s)
	}

	return s
}

func (p *Router) makePacket(dst utils.NodeID, typ string, payload []byte) (internal.Packet, error) {
	return internal.Packet{
		Dst:     dst,
		Src:     utils.NewNodeID(dst.NS, p.key.Digest()),
		Type:    typ,
		Payload: payload,
		TTL:     3,
	}, nil
}

func (p *Router) AddNode(info utils.NodeInfo) {
	p.dhtMutex.RLock()
	defer p.dhtMutex.RUnlock()
	p.mainDht.AddNode(info)
	for _, d := range p.groupDht {
		d.AddNode(info)
	}
}

func (p *Router) ActiveSessions() []utils.NodeInfo {
	var nodes []utils.NodeInfo
	p.sessionMutex.RLock()
	defer p.sessionMutex.RUnlock()
	for _, n := range p.KnownNodes() {
		if _, ok := p.sessions[n.ID.String()]; ok {
			nodes = append(nodes, n)
		}
	}
	return nodes
}

func (p *Router) KnownNodes() []utils.NodeInfo {
	var nodes []utils.NodeInfo
	nodes = append(nodes, p.mainDht.KnownNodes()...)
	for _, d := range p.groupDht {
		nodes = append(nodes, d.KnownNodes()...)
	}
	return nodes
}

func (p *Router) ID() utils.NodeID {
	return p.id
}

func (p *Router) Close() {
	p.exit <- 0
	p.mainDht.Close()
	for _, d := range p.groupDht {
		d.Close()
	}
}
