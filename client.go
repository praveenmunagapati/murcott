// Package murcott is a decentralized instant messaging framework.
package murcott

import (
	"time"

	"github.com/h2so5/murcott/client"
	"github.com/h2so5/murcott/log"
	"github.com/h2so5/murcott/node"
	"github.com/h2so5/murcott/utils"
	"gopkg.in/vmihailenco/msgpack.v2"
)

type Client struct {
	node       *node.Node
	msgHandler messageHandler
	status     client.UserStatus
	profile    client.UserProfile
	id         utils.NodeID
	Roster     *client.Roster
	Logger     *log.Logger
}

type messageHandler func(src utils.NodeID, msg client.ChatMessage)
type Message interface{}

// NewClient generates a Client with the given PrivateKey.
func NewClient(key *utils.PrivateKey, config utils.Config) (*Client, error) {
	logger := log.NewLogger()

	node, err := node.NewNode(key, logger, config)
	if err != nil {
		return nil, err
	}

	node.RegisterMessageType("chat", client.ChatMessage{})
	node.RegisterMessageType("ack", client.MessageAck{})
	node.RegisterMessageType("profile-req", client.UserProfileRequest{})
	node.RegisterMessageType("profile-res", client.UserProfileResponse{})
	node.RegisterMessageType("presence", client.UserPresence{})

	c := &Client{
		node:   node,
		status: client.UserStatus{Type: client.StatusOffline},
		id:     utils.NewNodeID([4]byte{1, 1, 1, 1}, key.Digest()),
		Roster: &client.Roster{},
		Logger: logger,
	}

	c.node.Handle(func(src utils.NodeID, msg interface{}) interface{} {
		switch msg.(type) {
		case client.ChatMessage:
			if c.msgHandler != nil {
				c.msgHandler(src, msg.(client.ChatMessage))
			}
			return client.MessageAck{}
		case client.UserProfileRequest:
			return client.UserProfileResponse{Profile: c.profile}
		case client.UserPresence:
			p := msg.(client.UserPresence)
			if !p.Ack {
				c.node.Send(src, client.UserPresence{Status: c.status, Ack: true})
			}
		}
		return nil
	})

	return c, nil
}

func Read() (Message, error) {
	return nil, nil
}

// Starts a mainloop in the current goroutine.
func (c *Client) Run() {
	c.node.Run()
}

// Stops the current mainloop.
func (c *Client) Close() {
	status := c.status
	status.Type = client.StatusOffline
	for _, n := range c.Roster.List() {
		c.node.Send(n, client.UserPresence{Status: status, Ack: false})
	}
	time.Sleep(100 * time.Millisecond)
	c.node.Close()
}

// Sends the given message to the destination node.
func (c *Client) SendMessage(dst utils.NodeID, msg client.ChatMessage) {
	c.node.Send(dst, msg)
}

// HandleMessages registers the given function as a massage handler.
func (c *Client) HandleMessages(handler func(src utils.NodeID, msg client.ChatMessage)) {
	c.msgHandler = handler
}

func (c *Client) ID() utils.NodeID {
	return c.id
}

func (c *Client) Nodes() int {
	return len(c.node.KnownNodes())
}

func (c *Client) MarshalCache() (data []byte, err error) {
	return msgpack.Marshal(c.node.KnownNodes())
}

func (c *Client) UnmarshalCache(data []byte) error {
	var nodes []utils.NodeInfo
	err := msgpack.Unmarshal(data, &nodes)
	for _, n := range nodes {
		c.node.AddNode(n)
	}
	return err
}
