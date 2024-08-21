package speer_test

import (
	"testing"

	"github.com/TcMits/speer"
	"github.com/pion/webrtc/v4"
)

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

type TestHandler struct {
	speer.NOOPHandler
	CandidateCalled        chan webrtc.ICECandidate
	ConnectionOpenedCalled chan struct{}
	ConnectionClosedCalled chan struct{}
}

func NewTestHandler() *TestHandler {
	return &TestHandler{
		CandidateCalled:        make(chan webrtc.ICECandidate, 10),
		ConnectionOpenedCalled: make(chan struct{}, 1),
		ConnectionClosedCalled: make(chan struct{}, 1),
	}
}

func (h *TestHandler) OnTrickleICECandidate(candidate webrtc.ICECandidate) {
	h.CandidateCalled <- candidate
}

func (h *TestHandler) ConnectionOpened() {
	h.ConnectionOpenedCalled <- struct{}{}
}

func (h *TestHandler) ConnectionClosed() {
	h.ConnectionClosedCalled <- struct{}{}
}

func TestSpeerWithoutTrickle(t *testing.T) {
	peer1Handler := NewTestHandler()
	peer1 := must(speer.NewPeer(speer.WithInitiator(true), speer.WithHandlers(peer1Handler)))
	defer func() {
		peer1.Close()

		// because peer2 closed first
		if peer1.AfterCloseErrors == nil {
			t.Error("peer1.AfterCloseErrors is nil")
		}
	}()
	peer2Handler := NewTestHandler()
	peer2 := must(speer.NewPeer(speer.WithHandlers(peer2Handler)))
	defer func() {
		peer2.Close()
		mustWithoutError(peer2.AfterCloseErrors)
	}()

	mustWithoutError(peer1.CreateOffer(func(offer webrtc.SessionDescription) {
		mustWithoutError(peer2.ReceiveOffer(offer, func(answer webrtc.SessionDescription) {
			mustWithoutError(peer1.ReceiveAnswer(answer))
		}))
	}))
	<-peer1Handler.ConnectionOpenedCalled
	if !peer1.IsConnected() {
		t.Error("peer2 is not connected")
	}

	<-peer2Handler.ConnectionOpenedCalled
	if !peer1.IsConnected() {
		t.Error("peer2 is not connected")
	}

	fromP1 := "Hello, peer2"
	n := must(peer1.Write([]byte(fromP1)))
	if n != len(fromP1) {
		t.Errorf("n is %d, want %d", n, len(fromP1))
	}

	bufP2 := make([]byte, len(fromP1))
	n = must(peer2.Read(bufP2))
	if n != len(fromP1) {
		t.Errorf("n is %d, want %d", n, len(fromP1))
	}

	if string(bufP2) != fromP1 {
		t.Errorf("bufP2 is %s, want %s", bufP2, fromP1)
	}

	fromP2 := "Hello, peer1"
	n = must(peer2.Write([]byte(fromP2)))
	if n != len(fromP2) {
		t.Errorf("n is %d, want %d", n, len(fromP2))
	}

	bufP1 := make([]byte, len(fromP2))
	n = must(peer1.Read(bufP1))
	if n != len(fromP2) {
		t.Errorf("n is %d, want %d", n, len(fromP2))
	}

	if string(bufP1) != fromP2 {
		t.Errorf("bufP1 is %s, want %s", bufP1, fromP2)
	}
}

func TestSpeerWithTrickle(t *testing.T) {
	peer1Handler := NewTestHandler()
	peer1 := must(speer.NewPeer(
		speer.WithInitiator(true),
		speer.WithTrickle(true),
		speer.WithHandlers(peer1Handler),
	))
	defer func() {
		peer1.Close()

		// because peer2 closed first
		if peer1.AfterCloseErrors == nil {
			t.Error("peer1.AfterCloseErrors is nil")
		}
	}()
	peer2Handler := NewTestHandler()
	peer2 := must(speer.NewPeer(
		speer.WithTrickle(true),
		speer.WithHandlers(peer2Handler),
	))
	defer func() {
		peer2.Close()
		mustWithoutError(peer2.AfterCloseErrors)
	}()

	mustWithoutError(peer1.CreateOffer(func(offer webrtc.SessionDescription) {
		mustWithoutError(peer2.ReceiveOffer(offer, func(answer webrtc.SessionDescription) {
			// because peer2 got remote description first
			go func() {
				for {
					select {
					case _ = <-peer1Handler.ConnectionClosedCalled:
						break
					case candidate := <-peer1Handler.CandidateCalled:
						peer2.AddICECandidate(candidate.ToJSON())
					}
				}
			}()

			mustWithoutError(peer1.ReceiveAnswer(answer))

			go func() {
				for {
					select {
					case _ = <-peer2Handler.ConnectionClosedCalled:
						break
					case candidate := <-peer2Handler.CandidateCalled:
						peer1.AddICECandidate(candidate.ToJSON())
					}
				}
			}()
		}))
	}))

	<-peer1Handler.ConnectionOpenedCalled
	if !peer1.IsConnected() {
		t.Error("peer2 is not connected")
	}

	<-peer2Handler.ConnectionOpenedCalled
	if !peer1.IsConnected() {
		t.Error("peer2 is not connected")
	}

	fromP1 := "Hello, peer2"
	n := must(peer1.Write([]byte(fromP1)))
	if n != len(fromP1) {
		t.Errorf("n is %d, want %d", n, len(fromP1))
	}

	bufP2 := make([]byte, len(fromP1))
	n = must(peer2.Read(bufP2))
	if n != len(fromP1) {
		t.Errorf("n is %d, want %d", n, len(fromP1))
	}

	if string(bufP2) != fromP1 {
		t.Errorf("bufP2 is %s, want %s", bufP2, fromP1)
	}

	fromP2 := "Hello, peer1"
	n = must(peer2.Write([]byte(fromP2)))
	if n != len(fromP2) {
		t.Errorf("n is %d, want %d", n, len(fromP2))
	}

	bufP1 := make([]byte, len(fromP2))
	n = must(peer1.Read(bufP1))
	if n != len(fromP2) {
		t.Errorf("n is %d, want %d", n, len(fromP2))
	}

	if string(bufP1) != fromP2 {
		t.Errorf("bufP1 is %s, want %s", bufP1, fromP2)
	}
}

func TestSpeerManualNegotiated(t *testing.T) {
	trueValue := true
	channelID := uint16(200)
	peer1Handler := NewTestHandler()
	peer1 := must(speer.NewPeer(
		speer.WithInitiator(true),
		speer.WithHandlers(peer1Handler),
		speer.WithChannelConfig(webrtc.DataChannelInit{Negotiated: &trueValue, ID: &channelID}),
	))
	defer func() {
		peer1.Close()

		// because peer2 closed first
		if peer1.AfterCloseErrors == nil {
			t.Error("peer1.AfterCloseErrors is nil")
		}
	}()
	peer2Handler := NewTestHandler()
	peer2 := must(speer.NewPeer(
		speer.WithHandlers(peer2Handler),
		speer.WithChannelConfig(webrtc.DataChannelInit{Negotiated: &trueValue, ID: &channelID}),
	))
	defer func() {
		peer2.Close()
		mustWithoutError(peer2.AfterCloseErrors)
	}()

	mustWithoutError(peer1.CreateOffer(func(offer webrtc.SessionDescription) {
		mustWithoutError(peer2.ReceiveOffer(offer, func(answer webrtc.SessionDescription) {
			mustWithoutError(peer1.ReceiveAnswer(answer))
		}))
	}))

	<-peer1Handler.ConnectionOpenedCalled
	if !peer1.IsConnected() {
		t.Error("peer2 is not connected")
	}

	<-peer2Handler.ConnectionOpenedCalled
	if !peer1.IsConnected() {
		t.Error("peer2 is not connected")
	}

	fromP1 := "Hello, peer2"
	n := must(peer1.Write([]byte(fromP1)))
	if n != len(fromP1) {
		t.Errorf("n is %d, want %d", n, len(fromP1))
	}

	bufP2 := make([]byte, len(fromP1))
	n = must(peer2.Read(bufP2))
	if n != len(fromP1) {
		t.Errorf("n is %d, want %d", n, len(fromP1))
	}

	if string(bufP2) != fromP1 {
		t.Errorf("bufP2 is %s, want %s", bufP2, fromP1)
	}

	fromP2 := "Hello, peer1"
	n = must(peer2.Write([]byte(fromP2)))
	if n != len(fromP2) {
		t.Errorf("n is %d, want %d", n, len(fromP2))
	}

	bufP1 := make([]byte, len(fromP2))
	n = must(peer1.Read(bufP1))
	if n != len(fromP2) {
		t.Errorf("n is %d, want %d", n, len(fromP2))
	}

	if string(bufP1) != fromP2 {
		t.Errorf("bufP1 is %s, want %s", bufP1, fromP2)
	}
}
