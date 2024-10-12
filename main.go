package main

import (
	"github.com/zijiren233/gwst/ws"
)

func main() {
	go func() {
		wsf := ws.NewWsForwarder("0.0.0.0:8080", ws.NewWsDialer(
			"127.0.0.1:8081",
			"/ws",
			ws.WithDialTLS("www.microstft.com", true),
		))
		err := wsf.Serve()
		if err != nil {
			panic(err)
		}
	}()
	wss := ws.NewWsServer("0.0.0.0:8081", "127.0.0.1:8082", "/ws", ws.WithTLS("", "", "www.microstft.com"))
	err := wss.Serve(ws.WithECC())
	if err != nil {
		panic(err)
	}
}
