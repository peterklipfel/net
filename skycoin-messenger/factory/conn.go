package factory

import (
	"crypto/aes"
	"encoding/json"
	"errors"
	"github.com/skycoin/net/conn"
	"github.com/skycoin/net/factory"
	"github.com/skycoin/skycoin/src/cipher"
	"sync"
	"sync/atomic"
	"time"
)

type Connection struct {
	*factory.Connection
	factory *MessengerFactory

	closed     bool
	key        cipher.PubKey
	keySetCond *sync.Cond
	keySet     bool
	secKey     cipher.SecKey
	targetKey  cipher.PubKey

	context sync.Map

	services    *NodeServices
	servicesMap map[cipher.PubKey]*Service
	fieldsMutex sync.RWMutex

	in chan []byte

	proxyConnections map[uint32]*Connection

	appTransports      map[cipher.PubKey]*Transport
	appTransportsMutex sync.RWMutex

	connectTime int64

	skipFactoryReg bool

	appMessages        []PriorityMsg
	appMessagesPty     Priority
	appMessagesReadCnt int
	appMessagesMutex   sync.RWMutex
	appFeedback        atomic.Value
	// callbacks

	// call after received response for FindServiceNodesByKeys
	findServiceNodesByKeysCallback func(resp *QueryResp)

	// call after received response for FindServiceNodesByAttributes
	findServiceNodesByAttributesCallback func(resp *QueryByAttrsResp)

	// call after received response for BuildAppConnection
	appConnectionInitCallback func(resp *AppConnResp) *AppFeedback

	onConnected    func(connection *Connection)
	onDisconnected func(connection *Connection)
	reconnect      func()
}

// Used by factory to spawn connections for server side
func newConnection(c *factory.Connection, factory *MessengerFactory) *Connection {
	connection := &Connection{
		Connection:    c,
		factory:       factory,
		appTransports: make(map[cipher.PubKey]*Transport),
	}
	c.RealObject = connection
	connection.keySetCond = sync.NewCond(connection.fieldsMutex.RLocker())
	return connection
}

// Used by factory to spawn connections for client side
func newClientConnection(c *factory.Connection, factory *MessengerFactory) *Connection {
	connection := &Connection{
		Connection:       c,
		factory:          factory,
		in:               make(chan []byte),
		proxyConnections: make(map[uint32]*Connection),
		appTransports:    make(map[cipher.PubKey]*Transport),
	}
	c.RealObject = connection
	connection.keySetCond = sync.NewCond(connection.fieldsMutex.RLocker())
	go func() {
		connection.preprocessor()
	}()
	return connection
}

// Used by factory to spawn connections for udp client side
func newUDPClientConnection(c *factory.Connection, factory *MessengerFactory) *Connection {
	connection := &Connection{
		Connection: c,
		factory:    factory,
		in:         make(chan []byte),
	}
	c.RealObject = connection
	connection.keySetCond = sync.NewCond(connection.fieldsMutex.RLocker())
	go func() {
		connection.preprocessor()
	}()
	return connection
}

// Used by factory to spawn connections for udp server side
func newUDPServerConnection(c *factory.Connection, factory *MessengerFactory) *Connection {
	connection := &Connection{
		Connection: c,
		factory:    factory,
	}
	c.RealObject = connection
	connection.keySetCond = sync.NewCond(connection.fieldsMutex.RLocker())
	return connection
}

func (c *Connection) setProxyConnection(seq uint32, conn *Connection) {
	c.fieldsMutex.Lock()
	c.proxyConnections[seq] = conn
	c.fieldsMutex.Unlock()
}

func (c *Connection) removeProxyConnection(seq uint32) (conn *Connection, ok bool) {
	c.fieldsMutex.Lock()
	conn, ok = c.proxyConnections[seq]
	if ok {
		delete(c.proxyConnections, seq)
	}
	c.fieldsMutex.Unlock()
	return
}

func (c *Connection) SetKey(key cipher.PubKey) {
	c.fieldsMutex.Lock()
	c.key = key
	c.keySet = true
	c.fieldsMutex.Unlock()
	c.keySetCond.Broadcast()
	if c.onConnected != nil {
		c.onConnected(c)
	}
}

func (c *Connection) IsKeySet() (b bool) {
	c.fieldsMutex.RLock()
	b = c.keySet
	c.fieldsMutex.RUnlock()
	return
}

func (c *Connection) GetKey() cipher.PubKey {
	c.fieldsMutex.RLock()
	defer c.fieldsMutex.RUnlock()
	if !c.keySet {
		c.keySetCond.Wait()
	}
	return c.key
}

func (c *Connection) SetSecKey(key cipher.SecKey) {
	c.fieldsMutex.Lock()
	c.secKey = key
	c.fieldsMutex.Unlock()
}

func (c *Connection) GetSecKey() (key cipher.SecKey) {
	c.fieldsMutex.RLock()
	key = c.secKey
	c.fieldsMutex.RUnlock()
	return
}

func (c *Connection) SetTargetKey(key cipher.PubKey) {
	c.fieldsMutex.Lock()
	c.targetKey = key
	c.fieldsMutex.Unlock()
}

func (c *Connection) GetTargetKey() (key cipher.PubKey) {
	c.fieldsMutex.RLock()
	key = c.targetKey
	c.fieldsMutex.RUnlock()
	return
}

func (c *Connection) setServices(s *NodeServices) {
	if s == nil {
		c.fieldsMutex.Lock()
		c.services = nil
		c.servicesMap = nil
		c.fieldsMutex.Unlock()
		return
	}
	m := make(map[cipher.PubKey]*Service)
	for _, v := range s.Services {
		m[v.Key] = v
	}
	c.fieldsMutex.Lock()
	c.services = s
	c.servicesMap = m
	c.fieldsMutex.Unlock()
}

func (c *Connection) getService(key cipher.PubKey) (service *Service, ok bool) {
	c.fieldsMutex.Lock()
	defer c.fieldsMutex.Unlock()
	service, ok = c.servicesMap[key]
	return
}

func (c *Connection) GetServices() *NodeServices {
	c.fieldsMutex.RLock()
	defer c.fieldsMutex.RUnlock()
	return c.services
}

func (c *Connection) Reg() error {
	return c.Write(GenRegMsg())
}

func (c *Connection) RegWithKey(key cipher.PubKey, context map[string]string) error {
	c.StoreContext(publicKey, key)
	return c.writeOPReq(OP_REG_KEY, &regWithKey{PublicKey: key, Context: context, Version: RegWithKeyAndEncryptionVersion})
}

func (c *Connection) RegWithKeys(key, target cipher.PubKey, context map[string]string) error {
	c.StoreContext(publicKey, key)
	c.SetTargetKey(target)
	return c.writeOPReq(OP_REG_KEY, &regWithKey{PublicKey: key, Context: context, Version: RegWithKeyAndEncryptionVersion})
}

// register services to discovery
func (c *Connection) UpdateServices(ns *NodeServices) error {
	c.setServices(ns)
	if ns == nil {
		ns = &NodeServices{}
	}
	err := c.writeOP(OP_OFFER_SERVICE, ns)
	if err != nil {
		return err
	}
	return nil
}

// register a service to discovery
func (c *Connection) OfferService(attrs ...string) error {
	return c.UpdateServices(&NodeServices{Services: []*Service{{Key: c.GetKey(), Attributes: attrs}}})
}

// register a service to discovery
func (c *Connection) OfferServiceWithAddress(address string, attrs ...string) error {
	return c.UpdateServices(&NodeServices{Services: []*Service{{Key: c.GetKey(), Attributes: attrs, Address: address}}})
}

// register a service to discovery
func (c *Connection) OfferPrivateServiceWithAddress(address string, allowNodes []string, attrs ...string) error {
	return c.UpdateServices(&NodeServices{
		Services: []*Service{{
			Key:               c.GetKey(),
			Attributes:        attrs,
			Address:           address,
			HideFromDiscovery: true,
			AllowNodes:        allowNodes,
		}}})
}

// register a service to discovery
func (c *Connection) OfferStaticServiceWithAddress(address string, attrs ...string) error {
	ns := &NodeServices{Services: []*Service{{Key: c.GetKey(), Attributes: attrs, Address: address}}}
	c.factory.discoveryRegister(c, ns)
	return c.UpdateServices(ns)
}

// find services by attributes
func (c *Connection) FindServiceNodesByAttributes(attrs ...string) error {
	return c.writeOP(OP_QUERY_BY_ATTRS, newQueryByAttrs(attrs))
}

// find services by attributes
func (c *Connection) FindServiceNodesWithSeqByAttributes(attrs ...string) (seq uint32, err error) {
	q := newQueryByAttrs(attrs)
	seq = q.Seq
	err = c.writeOP(OP_QUERY_BY_ATTRS, q)
	return
}

// find services nodes by service public keys
func (c *Connection) FindServiceNodesByKeys(keys []cipher.PubKey) error {
	return c.writeOP(OP_QUERY_SERVICE_NODES, newQuery(keys))
}

func (c *Connection) BuildAppConnection(node, app cipher.PubKey) error {
	return c.writeOP(OP_BUILD_APP_CONN, &appConn{Node: node, App: app})
}

func (c *Connection) Send(to cipher.PubKey, msg []byte) error {
	return c.Write(GenSendMsg(c.GetKey(), to, msg))
}

func (c *Connection) SendCustom(msg []byte) error {
	return c.writeOPBytes(OP_CUSTOM, msg)
}

func (c *Connection) preprocessor() (err error) {
	defer func() {
		if e := recover(); e != nil {
			c.GetContextLogger().Debugf("panic in preprocessor %v", e)
		}
		if err != nil {
			c.GetContextLogger().Debugf("preprocessor err %v", err)
		}
		c.Close()
	}()
OUTER:
	for {
		select {
		case m, ok := <-c.Connection.GetChanIn():
			if !ok {
				return
			}
			c.GetContextLogger().Debugf("preprocessor read %x", m)
			if len(m) < MSG_HEADER_END {
				return
			}
			opn := m[MSG_OP_BEGIN]
			if opn&RESP_PREFIX > 0 {
				i := int(opn &^ RESP_PREFIX)
				r := getResp(i)
				if r != nil {
					body := m[MSG_HEADER_END:]
					if len(body) > 0 {
						err = json.Unmarshal(body, r)
						if err != nil {
							return
						}
					}
					err = r.Run(c)
					c.GetContextLogger().Debugf("execute op %#v err %v", r, err)
					if err != nil {
						if err == ErrDetach {
							err = nil
							break OUTER
						}
						return
					}
					putResp(i, r)
					continue
				}
			}

			c.in <- m
		}
	}
	for {
		select {
		case m, ok := <-c.Connection.GetChanIn():
			if !ok {
				return
			}
			c.in <- m
		}
	}
}

func (c *Connection) GetChanIn() <-chan []byte {
	if c.in == nil {
		return c.Connection.GetChanIn()
	}
	return c.in
}

func (c *Connection) Close() {
	if c.reconnect != nil {
		go c.reconnect()
	}
	if c.onDisconnected != nil {
		c.onDisconnected(c)
	}
	c.keySetCond.Broadcast()
	c.fieldsMutex.Lock()
	defer c.fieldsMutex.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	if c.keySet {
		if !c.skipFactoryReg {
			c.factory.unregister(c.key, c)
		}
		c.keySet = false
	}
	if c.in != nil {
		close(c.in)
	}

	c.appTransportsMutex.RLock()
	defer c.appTransportsMutex.RUnlock()

	if len(c.appTransports) > 0 {
		for _, v := range c.appTransports {
			v.Close()
		}
	}

	c.Connection.Close()
}

func (c *Connection) WaitForKey() (err error) {
	ok := make(chan struct{})
	go func() {
		c.GetKey()
		close(ok)
	}()
	select {
	case <-time.After(15 * time.Second):
		c.Close()
		err = errors.New("reg timeout")
	case <-ok:
	}
	return err
}

func (c *Connection) writeOPBytes(op byte, body []byte) error {
	data := make([]byte, MSG_HEADER_END+len(body))
	data[MSG_OP_BEGIN] = op
	copy(data[MSG_HEADER_END:], body)
	return c.Write(data)
}

func (c *Connection) writeOP(op byte, object interface{}) error {
	js, err := json.Marshal(object)
	if err != nil {
		return err
	}
	c.GetContextLogger().Debugf("writeOP %#v", object)
	return c.writeOPBytes(op, js)
}

func (c *Connection) writeOPReq(op byte, object interface{}) error {
	body, err := json.Marshal(object)
	if err != nil {
		return err
	}
	c.GetContextLogger().Debugf("writeOP %#v", object)
	data := make([]byte, MSG_HEADER_END+len(body))
	data[MSG_OP_BEGIN] = op
	copy(data[MSG_HEADER_END:], body)
	return c.WriteReq(data)
}

func (c *Connection) writeOPResp(op byte, object interface{}) error {
	body, err := json.Marshal(object)
	if err != nil {
		return err
	}
	c.GetContextLogger().Debugf("writeOP %#v", object)
	data := make([]byte, MSG_HEADER_END+len(body))
	data[MSG_OP_BEGIN] = op
	copy(data[MSG_HEADER_END:], body)
	return c.WriteResp(data)
}

func (c *Connection) setTransport(to cipher.PubKey, tr *Transport) {
	c.appTransportsMutex.Lock()
	if tr == nil {
		delete(c.appTransports, to)
	} else {
		c.appTransports[to] = tr
	}
	c.appTransportsMutex.Unlock()
}

func (c *Connection) getTransport(to cipher.PubKey) (tr *Transport, ok bool) {
	c.appTransportsMutex.RLock()
	tr, ok = c.appTransports[to]
	c.appTransportsMutex.RUnlock()
	return
}

func (c *Connection) UpdateConnectTime() {
	atomic.StoreInt64(&c.connectTime, time.Now().Unix())
}

func (c *Connection) GetConnectTime() int64 {
	return atomic.LoadInt64(&c.connectTime)
}

func (c *Connection) EnableSkipFactoryReg() {
	c.fieldsMutex.Lock()
	c.skipFactoryReg = true
	c.fieldsMutex.Unlock()
}

func (c *Connection) IsSkipFactoryReg() (skip bool) {
	c.fieldsMutex.RLock()
	skip = c.skipFactoryReg
	c.fieldsMutex.RUnlock()
	return
}

func (c *Connection) ForEachTransport(fn func(t *Transport)) {
	c.appTransportsMutex.RLock()
	for _, tr := range c.appTransports {
		fn(tr)
	}
	c.appTransportsMutex.RUnlock()
}

func (c *Connection) StoreContext(key, value interface{}) {
	c.context.Store(key, value)
}

func (c *Connection) LoadContext(key interface{}) (value interface{}, ok bool) {
	return c.context.Load(key)
}

func (c *Connection) PutMessage(v PriorityMsg) bool {
	c.appMessagesMutex.Lock()
	if c.appMessagesPty > v.Priority {
		c.appMessagesMutex.Unlock()
		return false
	}
	v.Time = time.Now().Unix()
	c.appMessages = append(c.appMessages, v)
	c.appMessagesPty = v.Priority
	c.appMessagesMutex.Unlock()
	return true
}

// Get messages
func (c *Connection) GetMessages() (result []PriorityMsg) {
	c.appMessagesMutex.Lock()
	result = c.appMessages
	c.appMessagesReadCnt = len(result)
	c.appMessagesMutex.Unlock()
	return result
}

// Return unread messages count
func (c *Connection) CheckMessages() (result int) {
	c.appMessagesMutex.RLock()
	result = len(c.appMessages) - c.appMessagesReadCnt
	c.appMessagesMutex.RUnlock()
	return result
}

func (c *Connection) SetAppFeedback(fb *AppFeedback) {
	c.appFeedback.Store(fb)
}

func (c *Connection) GetAppFeedback() *AppFeedback {
	v, ok := c.appFeedback.Load().(*AppFeedback)
	if !ok {
		return nil
	}
	return v
}

func (c *Connection) SetCrypto(pk cipher.PubKey, sk cipher.SecKey, target cipher.PubKey, iv []byte) (err error) {
	c.fieldsMutex.Lock()
	defer c.fieldsMutex.Unlock()
	if c.Connection.GetCrypto() != nil {
		return
	}
	crypto := conn.NewCrypto(pk, sk)
	err = crypto.SetTargetKey(target)
	if err != nil {
		return
	}
	if len(iv) == aes.BlockSize {
		err = crypto.Init(iv)
		if err != nil {
			return
		}
	}
	c.Connection.SetCrypto(crypto)
	return
}
