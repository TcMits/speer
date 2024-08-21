package main

import (
	"github.com/TcMits/speer"
	"github.com/pion/webrtc/v4"
)

type Handler struct {
	speer.NOOPHandler
	ConnectionOpenedCalled chan struct{}
}

func NewHandler() *Handler {
	return &Handler{ConnectionOpenedCalled: make(chan struct{}, 1)}
}

func (h *Handler) ConnectionOpened() {
	h.ConnectionOpenedCalled <- struct{}{}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func mustWithoutError(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	peer1Handler := NewHandler()
	peer1 := must(speer.NewPeer(speer.WithInitiator(true), speer.WithHandlers(peer1Handler)))
	defer peer1.Close()

	peer2Handler := NewHandler()
	peer2 := must(speer.NewPeer(speer.WithHandlers(peer2Handler)))
	defer peer2.Close()

	mustWithoutError(peer1.CreateOffer(func(offer webrtc.SessionDescription) {
		mustWithoutError(peer2.ReceiveOffer(offer, func(answer webrtc.SessionDescription) {
			mustWithoutError(peer1.ReceiveAnswer(answer))
		}))
	}))
	<-peer1Handler.ConnectionOpenedCalled
	<-peer2Handler.ConnectionOpenedCalled

	// send data
	_ = must(peer1.Write([]byte("Hello, peer2")))

	// receive data
	buffer := make([]byte, 12)
	_, _ = peer2.Read(buffer)
	println(string(buffer))
}
