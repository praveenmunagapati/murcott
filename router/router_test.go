package router

import (
	"net"
	"testing"
	"time"

	"github.com/h2so5/murcott/utils"

	"github.com/h2so5/murcott/log"
)

var namespace = utils.GlobalNamespace

func TestRouterMessageExchange(t *testing.T) {
	logger := log.NewLogger()
	msg := "The quick brown fox jumps over the lazy dog"

	key1 := utils.GeneratePrivateKey()
	key2 := utils.GeneratePrivateKey()

	router1, err := NewRouter(key1, logger, utils.DefaultConfig)
	if err != nil {
		t.Fatal(err)
	}
	router1.Discover(utils.DefaultConfig.Bootstrap())

	router2, err := NewRouter(key2, logger, utils.DefaultConfig)
	if err != nil {
		t.Fatal(err)
	}
	router2.Discover(utils.DefaultConfig.Bootstrap())

	time.Sleep(100 * time.Millisecond)
	router1.SendMessage(utils.NewNodeID(namespace, key2.Digest()), []byte(msg))

	m, err := router2.RecvMessage()
	if err != nil {
		t.Errorf("router2: recvMessage() returns error")
	}
	if m.Node.Digest.Cmp(router1.key.Digest()) != 0 {
		t.Errorf("router2: wrong source id")
	}
	if string(m.Payload) != msg {
		t.Errorf("router2: wrong message body")
	}

	router2.SendMessage(utils.NewNodeID(namespace, router1.key.Digest()), []byte(msg))
	m, err = router1.RecvMessage()
	if err != nil {
		t.Errorf("router1: recvMessage() returns error")
	}
	if m.Node.Digest.Cmp(router2.key.Digest()) != 0 {
		t.Errorf("router1: wrong source id")
	}
	if string(m.Payload) != msg {
		t.Errorf("router1: wrong message body")
	}

	router1.Close()
	router2.Close()
}

func TestRouterRouteExchange(t *testing.T) {
	logger := log.NewLogger()
	msg := "The quick brown fox jumps over the lazy dog"

	key1 := utils.GeneratePrivateKey()
	key2 := utils.GeneratePrivateKey()
	key3 := utils.GeneratePrivateKey()

	router1, err := NewRouter(key1, logger, utils.DefaultConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer router1.Close()
	router1.Discover(utils.DefaultConfig.Bootstrap())

	router2, err := NewRouter(key2, logger, utils.DefaultConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer router2.Close()
	router2.Discover(utils.DefaultConfig.Bootstrap())

	time.Sleep(100 * time.Millisecond)
	router3, err := NewRouter(key3, logger, utils.DefaultConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer router3.Close()
	addr, _ := net.ResolveUDPAddr("udp", router1.listener.Addr().String())
	router3.Discover([]net.UDPAddr{net.UDPAddr{Port: addr.Port, IP: net.ParseIP("127.0.0.1")}})

	time.Sleep(100 * time.Millisecond)
	router3.SendMessage(utils.NewNodeID(namespace, key1.Digest()), []byte(msg))

	m, err := router1.RecvMessage()
	if err != nil {
		t.Errorf("router1: recvMessage() returns error")
	}
	if m.Node.Digest.Cmp(router3.key.Digest()) != 0 {
		t.Errorf("router1: wrong source id")
	}
	if string(m.Payload) != msg {
		t.Errorf("router1: wrong message body")
	}
}

func TestRouterGroup(t *testing.T) {
	var config = utils.Config{
		P: "9200-9300",
		B: []string{
			"localhost:9200-9300",
		},
	}

	logger := log.NewLogger()

	gkey1 := utils.GeneratePrivateKey()
	gkey2 := utils.GeneratePrivateKey()

	router1, err := NewRouter(utils.GeneratePrivateKey(), logger, config)
	if err != nil {
		t.Fatal(err)
	}
	router1.Join(utils.NewNodeID(utils.GroupNamespace, gkey1.Digest()))
	defer router1.Close()

	router2, err := NewRouter(utils.GeneratePrivateKey(), logger, config)
	if err != nil {
		t.Fatal(err)
	}
	router2.Join(utils.NewNodeID(utils.GroupNamespace, gkey1.Digest()))
	defer router2.Close()

	router3, err := NewRouter(utils.GeneratePrivateKey(), logger, config)
	if err != nil {
		t.Fatal(err)
	}
	router3.Join(utils.NewNodeID(utils.GroupNamespace, gkey1.Digest()))
	defer router3.Close()

	router4, err := NewRouter(utils.GeneratePrivateKey(), logger, config)
	if err != nil {
		t.Fatal(err)
	}
	router4.Join(utils.NewNodeID(utils.GroupNamespace, gkey2.Digest()))
	defer router4.Close()

	router5, err := NewRouter(utils.GeneratePrivateKey(), logger, config)
	if err != nil {
		t.Fatal(err)
	}
	router5.Join(utils.NewNodeID(utils.GroupNamespace, gkey2.Digest()))
	defer router5.Close()

	router1.Discover(utils.DefaultConfig.Bootstrap())
	router2.Discover(utils.DefaultConfig.Bootstrap())
	router3.Discover(utils.DefaultConfig.Bootstrap())
	router4.Discover(utils.DefaultConfig.Bootstrap())
	router5.Discover(utils.DefaultConfig.Bootstrap())

	time.Sleep(100 * time.Millisecond)

	msg := "The quick brown fox jumps over the lazy dog"
	router3.SendMessage(utils.NewNodeID(utils.GroupNamespace, gkey1.Digest()), []byte(msg))

	{
		m, err := router1.RecvMessage()
		if err != nil {
			t.Errorf("router1: recvMessage() returns error")
		}
		if m.Node.Digest.Cmp(router3.key.Digest()) != 0 {
			t.Errorf("router1: wrong source id")
		}
		if string(m.Payload) != msg {
			t.Errorf("router1: wrong message body")
		}
	}

	{
		m, err := router2.RecvMessage()
		if err != nil {
			t.Errorf("router2: recvMessage() returns error")
		}
		if m.Node.Digest.Cmp(router3.key.Digest()) != 0 {
			t.Errorf("router2: wrong source id")
		}
		if string(m.Payload) != msg {
			t.Errorf("router2: wrong message body")
		}
	}
}
