package dht

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/anoop-dhiman/lbry.go/v2/dht/bits"
	"github.com/anoop-dhiman/lbry.go/v2/extras/stop"
)

type tokenManager struct {
	secret        []byte
	prevSecret    []byte
	lock          *sync.RWMutex
	stop          *stop.Group
	tokenUpdateCh chan Tokens
}

func (tm *tokenManager) Start(interval time.Duration) {
	tm.lock = &sync.RWMutex{}
	tm.stop = stop.New()

	if len(tm.secret) != 64 {
		tm.secret = make([]byte, 64)
		tm.prevSecret = make([]byte, 64)
		tm.rotateSecret()
	}

	tm.stop.Add(1)
	go func() {
		defer tm.stop.Done()
		tick := time.NewTicker(interval)
		for {
			select {
			case <-tick.C:
				tm.rotateSecret()
			case <-tm.stop.Ch():
				return
			}
		}
	}()
}

func (tm *tokenManager) Stop() {
	tm.stop.StopAndWait()
}

func (tm *tokenManager) Get(nodeID bits.Bitmap, addr *net.UDPAddr) string {
	return genToken(tm.secret, nodeID, addr)
}

func (tm *tokenManager) Verify(token string, nodeID bits.Bitmap, addr *net.UDPAddr) bool {
	return token == genToken(tm.secret, nodeID, addr) || token == genToken(tm.prevSecret, nodeID, addr)
}

func genToken(secret []byte, nodeID bits.Bitmap, addr *net.UDPAddr) string {
	buf := bytes.Buffer{}
	buf.Write(nodeID[:])
	buf.Write(addr.IP)
	buf.WriteString(strconv.Itoa(addr.Port))
	buf.Write(secret)
	t := sha256.Sum256(buf.Bytes())
	return string(t[:])
}

// Tokens contains secret and prevSecret
type Tokens struct {
	Secret     []byte
	PrevSecret []byte
}

func (tm *tokenManager) rotateSecret() {
	tm.lock.Lock()
	defer tm.lock.Unlock()

	copy(tm.prevSecret, tm.secret)

	_, err := rand.Read(tm.secret)
	if err != nil {
		panic(err)
	}

	if tm.tokenUpdateCh != nil {
		tm.tokenUpdateCh <- Tokens{
			Secret:     tm.secret,
			PrevSecret: tm.prevSecret,
		}
	}
}
