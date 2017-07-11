package conn

import (
	"encoding/binary"
	"github.com/skycoin/net/msg"
	"net"
	"time"
	"sync/atomic"
)

const (
	MAX_UDP_PACKAGE_SIZE = 1024
)

type UDPConn struct {
	ConnCommonFields
	UdpConn *net.UDPConn
	addr    *net.UDPAddr
	In      chan []byte
	Out     chan []byte

	lastTime    int64
}

func NewUDPConn(c *net.UDPConn, addr *net.UDPAddr) *UDPConn {
	return &UDPConn{UdpConn: c, addr: addr, lastTime: time.Now().Unix(), In: make(chan []byte), Out: make(chan []byte), ConnCommonFields:NewConnCommonFileds()}
}

func (c *UDPConn) ReadLoop() error {
	panic("UDPConn unimplemented ReadLoop")
}

func (c *UDPConn) WriteLoop() (err error) {
	defer func() {
		if err != nil {
			c.SetStatusToError(err)
		}
	}()
	for {
		select {
		case m, ok := <-c.Out:
			if !ok {
				c.CTXLogger.Debug("udp conn closed")
				return nil
			}
			c.CTXLogger.Debugf("msg out %x", m)
			err := c.Write(m)
			if err != nil {
				c.CTXLogger.Debugf("write msg is failed %v", err)
				return err
			}
		}
	}
}

func (c *UDPConn) Write(bytes []byte) error {
	s := atomic.AddUint32(&c.seq, 1)
	m := msg.New(msg.TYPE_NORMAL, s, bytes)
	c.AddMsg(s, m)
	return c.WriteBytes(m.Bytes())
}

func (c *UDPConn) WriteBytes(bytes []byte) error {
	c.writeMutex.Lock()
	defer c.writeMutex.Unlock()
	_, err := c.UdpConn.WriteToUDP(bytes, c.addr)
	return err
}

func (c *UDPConn) Ack(seq uint32) error {
	resp := make([]byte, msg.MSG_SEQ_END)
	resp[msg.MSG_TYPE_BEGIN] = msg.TYPE_ACK
	binary.BigEndian.PutUint32(resp[msg.MSG_SEQ_BEGIN:], seq)
	return c.WriteBytes(resp)
}

func (c *UDPConn) GetChanOut() chan<- []byte {
	return c.Out
}

func (c *UDPConn) GetChanIn() <-chan []byte {
	return c.In
}

func (c *UDPConn) IsClosed() bool {
	c.fieldsMutex.RLock()
	defer c.fieldsMutex.RUnlock()
	return c.closed
}

func (c *UDPConn) GetLastTime() int64 {
	c.fieldsMutex.RLock()
	defer c.fieldsMutex.RUnlock()
	return c.lastTime
}

func (c *UDPConn) UpdateLastTime() {
	c.fieldsMutex.Lock()
	c.lastTime = time.Now().Unix()
	c.fieldsMutex.Unlock()
}

func (c *UDPConn) Close() {
	defer func() {
		if err := recover(); err != nil {
			c.CTXLogger.Debug("closing closed udpconn")
		}
	}()
	c.fieldsMutex.Lock()
	c.closed = true
	c.fieldsMutex.Unlock()
	close(c.In)
	close(c.Out)
}

func (c *UDPConn) GetNextSeq() uint32 {
	return atomic.AddUint32(&c.seq, 1)
}