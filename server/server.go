package server

import (
	"net"
	"github.com/skycoin/net/conn"
	"log"
	"github.com/skycoin/skycoin/src/cipher"
)

var (
	DefaultConnectionFactory = NewFactory()
)

type Server struct {
	TCPAddress string
	UDPAddress string
	Factory    *ConnectionFactory
}

func New(tcpAddress, udpAddress string) *Server {
	s := &Server{TCPAddress: tcpAddress, UDPAddress: udpAddress, Factory: DefaultConnectionFactory}
	DefaultConnectionFactory.ConnHandler = s.connHandler
	return s
}

func (server *Server) ListenTCP() error {
	addr, err := net.ResolveTCPAddr("tcp", server.TCPAddress)
	if err != nil {
		return err
	}
	ln, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return err
	}
	for {
		c, err := ln.AcceptTCP()
		if err != nil {
			return err
		}
		connection := server.Factory.CreateTCPConn(c)
		go connection.ReadLoop()
	}
}

func (server *Server) ListenUDP() error {
	addr, err := net.ResolveUDPAddr("udp", server.UDPAddress)
	if err != nil {
		return err
	}
	udp, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	udpc := NewServerUDPConn(udp, server.Factory)
	return udpc.ReadLoop()
}

func (server *Server) connHandler(connection conn.Connection) {
	for {
		select {
		case m, ok := <-connection.GetChanIn():
			if !ok {
				log.Println("conn closed")
				return
			}
			log.Printf("msg in %x", m)
			key := cipher.NewPubKey(m[:33])
			c := server.Factory.GetConn(key.Hex())
			if c == nil {
				log.Printf("pubkey not found in factory %x", m)
				continue
			}
			publicKey := connection.GetPublicKey()
			copy(m[:33], publicKey[:])
			c.Write(m)
		}
	}
}

