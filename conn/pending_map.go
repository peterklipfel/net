package conn

import (
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/skycoin/net/msg"
)

type PendingMap struct {
	Pending              map[uint32]*msg.Message
	sync.RWMutex
	ackedMessages        map[uint32]*msg.Message
	ackedMessagesMutex   sync.RWMutex
	lastMinuteAcked      map[uint32]*msg.Message
	lastMinuteAckedMutex sync.RWMutex

	statistics  string
}

func NewPendingMap() *PendingMap {
	pendingMap := &PendingMap{Pending: make(map[uint32]*msg.Message), ackedMessages: make(map[uint32]*msg.Message)}
	go pendingMap.analyse()
	return pendingMap
}

func (m *PendingMap) AddMsg(k uint32, v *msg.Message) {
	m.Lock()
	m.Pending[k] = v
	m.Unlock()
	v.Transmitted()
}

func (m *PendingMap) DelMsg(k uint32) (ok bool) {
	m.RLock()
	v, ok := m.Pending[k]
	m.RUnlock()

	if !ok {
		return
	}

	v.Acked()

	m.ackedMessagesMutex.Lock()
	m.ackedMessages[k] = v
	m.ackedMessagesMutex.Unlock()

	m.Lock()
	delete(m.Pending, k)
	m.Unlock()
	return
}

func (m *PendingMap) analyse() {
	ticker := time.NewTicker(time.Minute)
	for {
		select {
		case <-ticker.C:
			m.ackedMessagesMutex.Lock()
			m.lastMinuteAckedMutex.Lock()
			m.lastMinuteAcked = m.ackedMessages
			m.lastMinuteAckedMutex.Unlock()
			m.ackedMessages = make(map[uint32]*msg.Message)
			m.ackedMessagesMutex.Unlock()

			m.lastMinuteAckedMutex.RLock()
			if len(m.lastMinuteAcked) < 1 {
				m.lastMinuteAckedMutex.RUnlock()
				continue
			}
			var max, min int64
			sum := new(big.Int)
			bytesSent := 0
			for _, v := range m.lastMinuteAcked {
				latency := v.Latency.Nanoseconds()
				if max < latency {
					max = latency
				}
				if min == 0 || min > latency {
					min = latency
				}
				y := new(big.Int)
				y.SetInt64(latency)
				sum.Add(sum, y)

				bytesSent += v.TotalSize()
			}
			n := new(big.Int)
			n.SetInt64(int64(len(m.lastMinuteAcked)))
			avg := new(big.Int)
			avg.Div(sum, n)
			m.lastMinuteAckedMutex.RUnlock()

			m.statistics = fmt.Sprintf("sent: %d bytes, latency: max %d ns, min %d ns, avg %s ns, count %s", bytesSent, max, min, avg, n)
		}
	}
}

type UDPPendingMap struct {
	*PendingMap
	waitBits byte
	waitCond *sync.Cond
}

func NewUDPPendingMap() *UDPPendingMap {
	m := &UDPPendingMap{PendingMap: NewPendingMap()}
	m.waitCond = sync.NewCond(&m.RWMutex)
	go m.analyse()
	return m
}

func (m *UDPPendingMap) AddMsg(k uint32, v *msg.Message) {
	m.Lock()
	i := k % 8
	for m.waitBits&(1<<i) > 0 {
		m.waitCond.Wait()
	}
	m.Pending[k] = v
	m.waitBits |= 1 << i
	m.Unlock()
	v.Transmitted()
}

func (m *UDPPendingMap) DelMsgAndGetLossMsgs(k uint32) (ok bool, loss []*msg.Message) {
	m.Lock()
	v, ok := m.Pending[k]
	if !ok {
		m.Unlock()
		return
	}
	delete(m.Pending, k)
	i := k % 8
	m.waitBits &^= 1 << i
	var prev byte
	prev = ^(1 << i) & ^(1 << ((k - 1) % 8 ))
	// loss
	if m.waitBits&prev > 0 {
		for n := 7; n > 1; n-- {
			pk := k - uint32(n)
			if m.waitBits&(1<<(pk%8)) > 0 {
				l, ok := m.Pending[pk]
				if !ok {
					panic("udp pending map !ok")
				}
				loss = append(loss, l)
			}
		}
	}
	m.Unlock()
	m.waitCond.Broadcast()

	v.Acked()

	m.ackedMessagesMutex.Lock()
	m.ackedMessages[k] = v
	m.ackedMessagesMutex.Unlock()

	return
}
