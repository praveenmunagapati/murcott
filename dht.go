package murcott

import (
	"crypto/rand"
	"crypto/sha1"
	"errors"
	"github.com/vmihailenco/msgpack"
	"net"
	"sort"
	"sync"
)

type dhtRpcCallback func(*dhtRpcCommand, *net.UDPAddr)

type dhtPacket struct {
	dst     NodeId
	payload []byte
}

type dhtRpcReturn struct {
	command dhtRpcCommand
	addr    *net.UDPAddr
}

type rpcReturnMap struct {
	chmap map[string]chan *dhtRpcReturn
	mutex *sync.Mutex
}

func (p *rpcReturnMap) push(id string, c chan *dhtRpcReturn) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.chmap[id] = c
}

func (p *rpcReturnMap) pop(id string) chan *dhtRpcReturn {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if c, ok := p.chmap[id]; ok {
		delete(p.chmap, id)
		return c
	} else {
		return nil
	}
}

type keyValueStore struct {
	storage map[string]string
	mutex   *sync.Mutex
}

func (p *keyValueStore) set(key, value string) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.storage[key] = value
}

func (p *keyValueStore) get(key string) (string, bool) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if v, ok := p.storage[key]; ok {
		return v, true
	} else {
		return "", false
	}
}

type dht struct {
	info   nodeInfo
	table  nodeTable
	k      int
	kvs    keyValueStore
	rpcRet rpcReturnMap
	rpc    chan dhtPacket
	logger *Logger
	exit   chan struct{}
}

type dhtRpcCommand struct {
	Id     []byte                 `msgpack:"id"`
	Method string                 `msgpack:"method"`
	Args   map[string]interface{} `msgpack:"args"`
}

func (p *dhtRpcCommand) getArgs(k string, v ...interface{}) {
	b, err := msgpack.Marshal(p.Args[k])
	if err == nil {
		msgpack.Unmarshal(b, v...)
	}
}

func newDht(k int, info nodeInfo, logger *Logger) *dht {
	d := dht{
		info:  info,
		table: newNodeTable(k, info.Id),
		k:     k,
		kvs: keyValueStore{
			storage: make(map[string]string),
			mutex:   &sync.Mutex{},
		},
		rpcRet: rpcReturnMap{
			chmap: make(map[string]chan *dhtRpcReturn),
			mutex: &sync.Mutex{},
		},
		rpc:    make(chan dhtPacket, 100),
		logger: logger,
		exit:   make(chan struct{}),
	}
	return &d
}

func (p *dht) run() {
	for {
		select {
		case <-p.exit:
			return
		}
	}
}

func (p *dht) addNode(node nodeInfo) {
	p.table.insert(node)
	p.sendPing(node.Id)
}

func (p *dht) getNodeInfo(id NodeId) *nodeInfo {
	return p.table.find(id)
}

func (p *dht) storeValue(key string, value string) {
	hash := sha1.Sum([]byte(key))
	c := newRpcCommand("store", map[string]interface{}{
		"key":   key,
		"value": value,
	})
	for _, n := range p.findNearestNode(NewNodeId(hash)) {
		p.sendPacket(n.Id, c)
	}
}

func (p *dht) findNearestNode(findid NodeId) []nodeInfo {

	reqch := make(chan nodeInfo, 100)
	endch := make(chan struct{}, 100)

	f := func(id NodeId, command dhtRpcCommand) {
		ch := p.sendPacket(id, command)

		ret := <-ch
		if ret != nil {
			if _, ok := ret.command.Args["nodes"]; ok {
				var nodes []map[string]string
				ret.command.getArgs("nodes", &nodes)
				for _, n := range nodes {
					if id, ok := n["id"]; ok {
						if addr, err := net.ResolveUDPAddr("udp", n["addr"]); err == nil {
							var idary [20]byte
							copy(idary[:], []byte(id)[:20])
							node := nodeInfo{Id: NewNodeId(idary), Addr: addr}
							if node.Id.Cmp(p.info.Id) != 0 {
								p.table.insert(node)
								reqch <- node
							}
						}
					}
				}
			}
		}
		endch <- struct{}{}
	}

	res := make([]nodeInfo, 0)
	nodes := p.table.nearestNodes(findid)

	if len(nodes) == 0 {
		return res
	}

	for _, n := range nodes {
		reqch <- n
	}

	count := 0
	requested := make(map[string]nodeInfo)

loop:
	for {
		select {
		case node := <-reqch:
			if _, ok := requested[node.Id.String()]; !ok {
				requested[node.Id.String()] = node
				c := newRpcCommand("find-node", map[string]interface{}{
					"id": string(findid.Bytes()),
				})
				go f(node.Id, c)
				count++
			}
		case <-endch:
			count--
			if count == 0 {
				break loop
			}
		}
	}

	for _, v := range requested {
		res = append(res, v)
	}

	sorter := nodeInfoSorter{nodes: res, id: findid}
	sort.Sort(sorter)

	if len(sorter.nodes) > p.k {
		return sorter.nodes[:p.k]
	} else {
		return sorter.nodes
	}
}

func (p *dht) loadValue(key string) *string {

	if v, ok := p.kvs.get(key); ok {
		return &v
	}

	hash := sha1.Sum([]byte(key))
	keyid := NewNodeId(hash)

	retch := make(chan *string, 2)
	reqch := make(chan NodeId, 100)
	endch := make(chan struct{}, 100)

	nodes := p.table.nearestNodes(NewNodeId(hash))

	f := func(id NodeId, keyid NodeId, command dhtRpcCommand) {
		ch := p.sendPacket(id, command)

		ret := <-ch
		if ret != nil {
			if val, ok := ret.command.Args["value"].(string); ok {
				retch <- &val
			} else if _, ok := ret.command.Args["nodes"]; ok {

				var nodes []map[string]string
				ret.command.getArgs("nodes", &nodes)
				dist := id.Xor(keyid)
				for _, n := range nodes {
					if id, ok := n["id"]; ok {
						if addr, err := net.ResolveUDPAddr("udp", n["addr"]); err == nil {
							var idary [20]byte
							copy(idary[:], []byte(id)[:20])
							node := NewNodeId(idary)
							p.table.insert(nodeInfo{Id: node, Addr: addr})
							if dist.Cmp(node.Xor(keyid)) == 1 {
								reqch <- node
							}
						}
					}
				}
			}
		}
		endch <- struct{}{}
	}

	if len(nodes) == 0 {
		return nil
	}

	for _, n := range nodes {
		reqch <- n.Id
	}

	count := 0
	requested := make(map[string]struct{})

	for {
		select {
		case id := <-reqch:
			if _, ok := requested[id.String()]; !ok {
				requested[id.String()] = struct{}{}
				c := newRpcCommand("find-value", map[string]interface{}{
					"key": key,
				})
				go f(id, keyid, c)
				count++
			}
		case <-endch:
			count--
			if count == 0 {
				select {
				case data := <-retch:
					return data
				default:
					return nil
				}
			}
		case data := <-retch:
			return data
		default:
		}
	}
}

func (p *dht) processPacket(src nodeInfo, payload []byte) {
	var command dhtRpcCommand
	err := msgpack.Unmarshal(payload, &command)
	if err == nil {
		p.table.insert(src)

		switch command.Method {
		case "ping":
			p.logger.Info("Receive DHT Ping from %s", src.Id.String())
			p.sendPacket(src.Id, newRpcReturnCommand(command.Id, nil))

		case "find-node":
			p.logger.Info("Receive DHT Find-Node from %s", src.Id.String())
			if id, ok := command.Args["id"].(string); ok {
				args := map[string]interface{}{}
				var idary [20]byte
				copy(idary[:], []byte(id)[:20])
				nodes := p.table.nearestNodes(NewNodeId(idary))
				ary := make([]map[string]string, len(nodes))
				for i, n := range nodes {
					ary[i] = map[string]string{
						"id":   string(n.Id.Bytes()),
						"addr": n.Addr.String(),
					}
				}
				args["nodes"] = ary
				p.sendPacket(src.Id, newRpcReturnCommand(command.Id, args))
			}

		case "store":
			p.logger.Info("Receive DHT Store from %s", src.Id.String())
			if key, ok := command.Args["key"].(string); ok {
				if val, ok := command.Args["value"].(string); ok {
					p.kvs.set(key, val)
				}
			}

		case "find-value":
			p.logger.Info("Receive DHT Find-Node from %s", src.Id.String())
			if key, ok := command.Args["key"].(string); ok {
				args := map[string]interface{}{}
				if val, ok := p.kvs.get(key); ok {
					args["value"] = val
				} else {
					hash := sha1.Sum([]byte(key))
					nodes := p.table.nearestNodes(NewNodeId(hash))
					ary := make([]map[string]string, len(nodes))
					for i, n := range nodes {
						ary[i] = map[string]string{
							"id":   string(n.Id.Bytes()),
							"addr": n.Addr.String(),
						}
					}
					args["nodes"] = ary
				}
				p.sendPacket(src.Id, newRpcReturnCommand(command.Id, args))
			}

		case "": // callback
			id := string(command.Id)
			if ch := p.rpcRet.pop(id); ch != nil {
				ch <- &dhtRpcReturn{command: command, addr: src.Addr}
			}
		}
	}
}

func (p *dht) nextPacket() (NodeId, []byte, error) {
	if c, ok := <-p.rpc; ok {
		return c.dst, c.payload, nil
	} else {
		return NodeId{}, nil, errors.New("DHT closed")
	}
}

func newRpcCommand(method string, args map[string]interface{}) dhtRpcCommand {
	id := make([]byte, 20)
	_, err := rand.Read(id)
	if err != nil {
		panic(err)
	}
	return dhtRpcCommand{
		Id:     id,
		Method: method,
		Args:   args,
	}
}

func newRpcReturnCommand(id []byte, args map[string]interface{}) dhtRpcCommand {
	return dhtRpcCommand{
		Id:     id,
		Method: "",
		Args:   args,
	}
}

func (p *dht) sendPing(dst NodeId) {
	c := newRpcCommand("ping", nil)
	ch := p.sendPacket(dst, c)
	go func() {
		ret := <-ch
		if ret != nil {
			p.logger.Info("Receive DHT ping response")
		}
	}()
}

func (p *dht) sendPacket(dst NodeId, command dhtRpcCommand) chan *dhtRpcReturn {
	data, err := msgpack.Marshal(command)
	if err != nil {
		panic(err)
	}

	id := string(command.Id)
	ch := make(chan *dhtRpcReturn, 2)

	p.rpcRet.push(id, ch)
	p.rpc <- dhtPacket{dst: dst, payload: data}

	return ch
}

func (p *dht) close() {
	p.exit <- struct{}{}
	close(p.rpc)
}
