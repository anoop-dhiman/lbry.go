package dht

import (
	"net"
	"testing"

	"github.com/anoop-dhiman/lbry.go/v2/dht/bits"
)

func TestBootstrapPing(t *testing.T) {
	b := NewBootstrapNode(bits.Rand(), 10, bootstrapDefaultRefreshDuration)

	listener, err := net.ListenPacket(Network, "127.0.0.1:54320")
	if err != nil {
		panic(err)
	}

	err = b.Connect(listener.(*net.UDPConn))
	if err != nil {
		t.Error(err)
	}

	b.Shutdown()
}
