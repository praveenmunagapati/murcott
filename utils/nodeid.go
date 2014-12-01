package utils

import (
	"crypto/rand"
	"math/big"
	"reflect"

	"github.com/tv42/base58"
	"github.com/vmihailenco/msgpack"
)

func init() {
	msgpack.Register(reflect.TypeOf(NodeID{}),
		func(e *msgpack.Encoder, v reflect.Value) error {
			id := v.Interface().(NodeID)
			return e.EncodeBytes(id.i.Bytes())
		},
		func(d *msgpack.Decoder, v reflect.Value) error {
			b, err := d.DecodeBytes()
			if err != nil {
				return nil
			}
			i := big.NewInt(0)
			i.SetBytes(b)
			if i.BitLen() > 160 {
				return nil
			}
			v.Set(reflect.ValueOf(NodeID{*i}))
			return nil
		})
}

// NodeID represents a 160-bit node identifier.
type NodeID struct {
	i big.Int
}

// NewNodeID generates NodeID from the given big-endian byte array.
func NewNodeID(data [20]byte) NodeID {
	i := big.NewInt(0)
	i.SetBytes(data[:])
	return NodeID{*i}
}

// NewNodeIDFromString generates NodeID from the given base58-encoded string.
func NewNodeIDFromString(str string) (NodeID, error) {
	i, err := base58.DecodeToBig([]byte(str))
	if err != nil {
		return NodeID{}, err
	}
	return NodeID{*i}, nil
}

func NewRandomNodeID() NodeID {
	var data [20]byte
	_, err := rand.Read(data[:])
	if err != nil {
		panic(err)
	} else {
		return NewNodeID(data)
	}
}

func (id NodeID) Xor(n NodeID) NodeID {
	d := big.NewInt(0)
	return NodeID{i: *d.Xor(&id.i, &n.i)}
}

func (id NodeID) BitLen() int {
	return 160
}

func (id NodeID) Bit(i int) uint {
	return id.i.Bit(159 - i)
}

func (id NodeID) Cmp(n NodeID) int {
	return id.i.Cmp(&n.i)
}

func (id NodeID) Log2int() int {
	l := 159
	b := big.NewInt(0).Add(&id.i, big.NewInt(1))
	for i := 160; i >= 0 && b.Bit(i) == 0; i-- {
		l--
	}
	if l < 0 {
		return 0
	}
	return l
}

// Bytes returns identifier as a big-endian byte array.
func (id NodeID) Bytes() []byte {
	return id.i.Bytes()
}

// String returns identifier as a base58-encoded byte array.
func (id NodeID) String() string {
	return string(base58.EncodeBig(nil, &id.i))
}