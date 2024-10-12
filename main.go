package main

import (
	"fmt"
	"net"
	"time"

	"github.com/zijiren233/gwst/ws"
)

func main() {
	go func() {
		wss := ws.NewWsServer("0.0.0.0:8080", "127.0.0.1:8081", "www.microstft.com", "/ws", ws.WithTLS("", ""))
		err := wss.Serve(ws.WithECC())
		if err != nil {
			panic(err)
		}
	}()

	go func() {
		ln, err := net.Listen("tcp", "127.0.0.1:8081")
		if err != nil {
			panic(err)
		}
		defer ln.Close()
		for {
			conn, err := ln.Accept()
			if err != nil {
				panic(err)
			}
			fmt.Println("accepted tcp")
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if err != nil {
						panic(err)
					}
					fmt.Println("tcp", string(buf[:n]))
				}
			}(conn)
		}
	}()

	go func() {
		ln, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8081})
		if err != nil {
			panic(err)
		}
		defer ln.Close()
		for {
			buf := make([]byte, 1024)
			n, _, err := ln.ReadFromUDP(buf)
			if err != nil {
				panic(err)
			}
			fmt.Println("udp", string(buf[:n]))
		}
	}()

	time.Sleep(time.Second * 2)

	go func() {
		wsc := ws.NewWsClient("127.0.0.1:8080", "/ws", ws.WithDialTLS(true))
		conn, err := wsc.DialTCP()
		if err != nil {
			panic(err)
		}
		defer conn.Close()
		fmt.Println("connected")
		for {
			conn.Write([]byte("hello"))
			time.Sleep(time.Second)
		}
	}()

	wsc := ws.NewWsClient("127.0.0.1:8080", "/ws", ws.WithDialTLS(true))
	conn, err := wsc.DialUDP()
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	fmt.Println("connected")
	for {
		conn.Write([]byte("hello"))
		time.Sleep(time.Second)
	}
}
