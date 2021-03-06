package dht

import (
	"sync"

	"github.com/h2so5/murcott/utils"
)

const bucketSize = 160

type nodeTable struct {
	buckets [][]utils.NodeInfo
	selfid  utils.NodeID
	k       int
	mutex   *sync.RWMutex
}

func newNodeTable(k int, id utils.NodeID) nodeTable {
	buckets := make([][]utils.NodeInfo, bucketSize)

	return nodeTable{
		buckets: buckets,
		selfid:  id,
		k:       k,
		mutex:   &sync.RWMutex{},
	}
}

func (p *nodeTable) insert(node utils.NodeInfo) {
	p.remove(node.ID)

	p.mutex.Lock()
	defer p.mutex.Unlock()

	b := node.ID.Digest.Xor(p.selfid.Digest).Log2int()

	if len(p.buckets[b]) < p.k {
		p.buckets[b] = append(p.buckets[b], node)
	} else {
		p.buckets[b][len(p.buckets[b])-1] = node
	}
}

func (p *nodeTable) remove(id utils.NodeID) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	b := id.Digest.Xor(p.selfid.Digest).Log2int()
	for i, n := range p.buckets[b] {
		if n.ID.Digest.Cmp(id.Digest) == 0 {
			p.buckets[b] = append(p.buckets[b][:i], p.buckets[b][i+1:]...)
			return
		}
	}
}

func (p *nodeTable) nodes() []utils.NodeInfo {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	var i []utils.NodeInfo
	for _, b := range p.buckets {
		for _, n := range b {
			i = append(i, n)
		}
	}
	return i
}

func (p *nodeTable) fingerNodes() []utils.NodeInfo {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	var nodes []utils.NodeInfo
	i := 0
loop:
	for ; i < bucketSize; i++ {
		for _, n := range p.buckets[i] {
			nodes = append(nodes, n)
			if len(nodes) >= p.k {
				break loop
			}
		}
	}
	for ; i < bucketSize; i++ {
		if len(p.buckets[i]) > 0 {
			nodes = append(nodes, p.buckets[i][0])
		}
	}
	return nodes
}

func (p *nodeTable) nearestNodes(id utils.NodeID) []utils.NodeInfo {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	var n []utils.NodeInfo
	b := id.Digest.Xor(p.selfid.Digest).Log2int()
	n = append(n, p.buckets[b]...)
	if len(n) > p.k {
		return n[len(n)-p.k:]
	}
	for i := 0; i < bucketSize; i++ {
		rb := b + i
		if rb < bucketSize {
			n = append(n, p.buckets[rb]...)
		}
		lb := b - i
		if lb >= 0 {
			n = append(n, p.buckets[lb]...)
		}
		if len(n) >= p.k {
			return n[len(n)-p.k:]
		}
	}
	return n
}

func (p *nodeTable) find(id utils.NodeID) *utils.NodeInfo {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	b := id.Digest.Xor(p.selfid.Digest).Log2int()
	for _, n := range p.buckets[b] {
		if n.ID.Digest.Cmp(id.Digest) == 0 {
			return &n
		}
	}
	return nil
}
