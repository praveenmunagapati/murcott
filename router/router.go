package router

import (
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
	ID      utils.NodeID
	Payload []byte
}

type Router struct {
	dht      *dht.DHT
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
	dht := dht.NewDHT(10, utils.NewNodeID([4]byte{1, 1, 1, 1}, key.Digest()), listener.RawConn, logger)

	logger.Info("Node ID: %s", key.Digest().String())
	logger.Info("Node Socket: %v", listener.Addr())

	r := Router{
		listener: listener,
		key:      key,
		sessions: make(map[string]*session),

		dht:    dht,
		logger: logger,
		recv:   make(chan Message, 100),
		send:   make(chan internal.Packet, 100),
		exit:   exit,
	}

	go r.run()
	return &r, nil
}

func (p *Router) Discover(addrs []net.UDPAddr) {
	for _, addr := range addrs {
		p.dht.Discover(&addr)
		p.logger.Info("Sent discovery packet to %v:%d", addr.IP, addr.Port)
	}
}

func (p *Router) SendMessage(dst utils.NodeID, payload []byte) error {
	pkt, err := p.makePacket(dst, "msg", payload)
	if err != nil {
		return err
	}
	p.send <- pkt
	return nil
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

	for {
		select {
		case s := <-acceptch:
			p.addSession(s)
		case pkt := <-p.send:
			s := p.getSession(pkt.Dst)
			if s != nil {
				err := s.Write(pkt)
				if err != nil {
					p.logger.Error("%v", err)
					p.removeSession(s)
					p.queuedPackets = append(p.queuedPackets, pkt)
				}
			} else {
				p.logger.Error("Route not found: %v", pkt.Dst)
				p.queuedPackets = append(p.queuedPackets, pkt)
			}
		case <-time.After(time.Second):
			var rest []internal.Packet
			for _, pkt := range p.queuedPackets {
				p.dht.FindNearestNode(pkt.Dst)
				s := p.getSession(pkt.Dst)
				if s != nil {
					err := s.Write(pkt)
					if err != nil {
						p.logger.Error("%v", err)
						p.removeSession(s)
						p.queuedPackets = append(p.queuedPackets, pkt)
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
			p.logger.Error("%v", err)
			p.removeSession(s)
			return
		}
		if pkt.Type == "msg" {
			p.recv <- Message{ID: pkt.Src, Payload: pkt.Payload}
		}
	}
}

func (p *Router) getSession(id utils.NodeID) *session {
	idstr := id.String()
	p.sessionMutex.RLock()
	if s, ok := p.sessions[idstr]; ok {
		p.sessionMutex.RUnlock()
		return s
	}
	p.sessionMutex.RUnlock()

	info := p.dht.GetNodeInfo(id)
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
		Src:     utils.NewNodeID([4]byte{1, 1, 1, 1}, p.key.Digest()),
		Type:    typ,
		Payload: payload,
	}, nil
}

func (p *Router) AddNode(info utils.NodeInfo) {
	p.dht.AddNode(info)
}

func (p *Router) KnownNodes() []utils.NodeInfo {
	return p.dht.KnownNodes()
}

func (p *Router) Close() {
	p.exit <- 0
	p.dht.Close()
}
