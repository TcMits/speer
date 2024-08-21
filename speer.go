package speer

import (
	"errors"
	"fmt"
	"io"
	"math/rand"

	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v4"
)

var api *webrtc.API

func init() {
	s := webrtc.SettingEngine{}
	s.DetachDataChannels()
	api = webrtc.NewAPI(webrtc.WithSettingEngine(s))
}

const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ123456789"

func randString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letterBytes[rand.Intn(len(letterBytes))]
	}
	return string(b)
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

type Handler interface {
	// OnTrickleICECandidate is called when a new ICE candidate is gathered.
	OnTrickleICECandidate(webrtc.ICECandidate)

	// OnConnectionOpened is called when the connection is established.
	ConnectionOpened()

	// OnConnectionClosed is called when the connection is closed.
	ConnectionClosed()

	// SDPTransform will be called before set local description
	SDPTransform(webrtc.SessionDescription) webrtc.SessionDescription
}

var _ Handler = (*NOOPHandler)(nil)

type NOOPHandler struct{}

func (NOOPHandler) OnTrickleICECandidate(webrtc.ICECandidate) {}

func (NOOPHandler) ConnectionOpened() {}

func (NOOPHandler) ConnectionClosed() {}

func (NOOPHandler) SDPTransform(sdp webrtc.SessionDescription) webrtc.SessionDescription { return sdp }

type options struct {
	initiator     bool
	config        webrtc.Configuration
	channelConfig webrtc.DataChannelInit
	offerOptions  webrtc.OfferOptions
	answerOptions webrtc.AnswerOptions
	trickle       bool
	handlers      []Handler
}

type peerOption func(*options)

func WithInitiator(initiator bool) peerOption {
	return func(o *options) {
		o.initiator = initiator
	}
}

func WithConfig(config webrtc.Configuration) peerOption {
	return func(o *options) {
		o.config = config
	}
}

func WithChannelConfig(config webrtc.DataChannelInit) peerOption {
	return func(o *options) {
		o.channelConfig = config
	}
}

func WithOfferOptions(opts webrtc.OfferOptions) peerOption {
	return func(o *options) {
		o.offerOptions = opts
	}
}

func WithAnswerOptions(opts webrtc.AnswerOptions) peerOption {
	return func(o *options) {
		o.answerOptions = opts
	}
}

func WithTrickle(t bool) peerOption {
	return func(o *options) {
		o.trickle = t
	}
}

func WithHandlers(h ...Handler) peerOption {
	return func(o *options) {
		for _, handler := range h {
			if handler != nil {
				o.handlers = append(o.handlers, handler)
			}
		}
	}
}

var (
	_ io.Writer = (*Peer)(nil)
	_ io.Reader = (*Peer)(nil)
	_ io.Closer = (*Peer)(nil)
)

type Peer struct {
	opts             options
	peerConnection   *webrtc.PeerConnection
	channel          *webrtc.DataChannel
	channelRWC       datachannel.ReadWriteCloser
	AfterCloseErrors error
}

func NewPeer(opts ...peerOption) (*Peer, error) {
	p := &Peer{
		opts: options{
			config: webrtc.Configuration{
				ICEServers: []webrtc.ICEServer{
					{URLs: []string{
						"stun:stun.l.google.com:19302",
						"stun:global.stun.twilio.com:3478",
					}},
				},
				SDPSemantics: webrtc.SDPSemanticsUnifiedPlan,
			},
		},
	}

	for _, o := range opts {
		o(&p.opts)
	}

	inner, err := api.NewPeerConnection(p.opts.config)
	if err != nil {
		return nil, err
	}
	p.peerConnection = inner
	p.peerConnection.OnICECandidate(func(c *webrtc.ICECandidate) { p.onICECandidate(c) })
	p.peerConnection.OnConnectionStateChange(func(pcs webrtc.PeerConnectionState) {
		if pcs == webrtc.PeerConnectionStateClosed {
			for _, handler := range p.opts.handlers {
				handler.ConnectionClosed()
			}
		}
	})

	if p.opts.initiator || (p.opts.channelConfig.Negotiated != nil && *p.opts.channelConfig.Negotiated) {
		channel, err := p.peerConnection.CreateDataChannel(randString(20), &p.opts.channelConfig)
		if err != nil {
			return nil, err
		}

		p.setupData(channel)
	} else {
		p.peerConnection.OnDataChannel(func(c *webrtc.DataChannel) { p.setupData(c) })
	}

	return p, nil
}

func (p *Peer) Close() error {
	p.close()
	return p.AfterCloseErrors
}

func (p *Peer) close(errs ...error) {
	if p.IsClosed() {
		return
	}

	if p.channelRWC != nil {
		errs = append(errs, p.channelRWC.Close())
	}

	if p.channel != nil {
		errs = append(errs, p.channel.Close())
	}

	errs = append(errs, p.peerConnection.Close())
	p.AfterCloseErrors = errors.Join(errs...)
}

func (p *Peer) CreateOffer(cb func(webrtc.SessionDescription)) error {
	if !p.opts.initiator {
		return errors.New("speer: peer is not an initiator")
	}

	offer, err := p.peerConnection.CreateOffer(&p.opts.offerOptions)
	if err != nil {
		return fmt.Errorf("speer: failed to create offer: %w", err)
	}

	for _, transform := range p.opts.handlers {
		offer = transform.SDPTransform(offer)
	}

	gatherComplete := webrtc.GatheringCompletePromise(p.peerConnection)
	err = p.peerConnection.SetLocalDescription(offer)
	if err != nil {
		return fmt.Errorf("speer: failed to create offer: %w", err)
	}

	if p.opts.trickle {
		cb(*p.peerConnection.LocalDescription())
		return nil
	}

	go func() {
		<-gatherComplete
		cb(*p.peerConnection.LocalDescription())
	}()
	return nil
}

func (p *Peer) ReceiveOffer(sdp webrtc.SessionDescription, cb func(webrtc.SessionDescription)) error {
	if p.opts.initiator {
		return errors.New("speer: peer is an initiator")
	}

	err := p.peerConnection.SetRemoteDescription(sdp)
	if err != nil {
		return fmt.Errorf("speer: failed to set remote description: %w", err)
	}

	answer, err := p.peerConnection.CreateAnswer(&p.opts.answerOptions)
	if err != nil {
		return fmt.Errorf("speer: failed to create answer: %w", err)
	}

	for _, transform := range p.opts.handlers {
		answer = transform.SDPTransform(answer)
	}

	gatherComplete := webrtc.GatheringCompletePromise(p.peerConnection)
	err = p.peerConnection.SetLocalDescription(answer)
	if err != nil {
		return fmt.Errorf("speer: failed to set local description: %w", err)
	}

	if p.opts.trickle {
		cb(*p.peerConnection.LocalDescription())
		return nil
	}

	go func() {
		<-gatherComplete
		cb(*p.peerConnection.LocalDescription())
	}()
	return nil
}

func (p *Peer) ReceiveAnswer(sdp webrtc.SessionDescription) error {
	if !p.opts.initiator {
		return errors.New("speer: peer is not an initiator")
	}
	return p.peerConnection.SetRemoteDescription(sdp)
}

func (p *Peer) AddICECandidate(candidate webrtc.ICECandidateInit) error {
	return p.peerConnection.AddICECandidate(candidate)
}

func (p *Peer) onICECandidate(candidate *webrtc.ICECandidate) {
	if candidate == nil {
		return
	}

	if p.opts.trickle {
		for _, handler := range p.opts.handlers {
			handler.OnTrickleICECandidate(*candidate)
		}
	}
}

func (p *Peer) IsConnected() bool {
	return !p.IsClosed() &&
		p.channel != nil &&
		p.channelRWC != nil &&
		p.channel.ReadyState() == webrtc.DataChannelStateOpen
}

func (p *Peer) IsClosed() bool {
	return p.peerConnection.ConnectionState() == webrtc.PeerConnectionStateClosed
}

func (p *Peer) setupData(channel *webrtc.DataChannel) {
	p.channel = channel
	p.channel.SetBufferedAmountLowThreshold(64 * 1024)
	p.channel.OnError(func(err error) { p.close(err) })
	p.channel.OnOpen(func() {
		p.channelRWC = must(channel.Detach()) // can't error
		for _, handler := range p.opts.handlers {
			handler.ConnectionOpened()
		}
	})
	p.channel.OnClose(func() { p.close() })
}

func (p *Peer) Write(b []byte) (int, error) {
	if !p.IsConnected() {
		return 0, errors.New("speer: peer is not connected")
	}
	return p.channelRWC.Write(b)
}

func (p *Peer) Read(b []byte) (int, error) {
	if !p.IsConnected() {
		return 0, errors.New("speer: peer is not connected")
	}
	return p.channelRWC.Read(b)
}
