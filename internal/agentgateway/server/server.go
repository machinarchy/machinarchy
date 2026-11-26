// Package server is the agent-facing gRPC service of the agent gateway
// (ADR-059). Agents reach the controller only through it: they never see the
// internal bus and hold no broker credential.
//
// Message bodies are the signed agentv1 envelopes, carried as opaque bytes
// (see codec.go), so authenticity does not depend on the transport. Methods:
//
//	Register   unary   signed registration (single-use enrolment token)
//	Renew      unary   signed lease renewal
//	SendResult unary   signed result, requires a live lease
//	Commands   stream  the agent opens it with a signed, lease-bound request
//	                   and then receives signed commands, acknowledging each
//
// The authenticated identity of a Commands stream is the agent ID proven by
// the opening request; it is never taken from later messages.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
	"github.com/machinarchy/machinarchy/internal/agentgateway"
)

// ServiceName is the gRPC service name agents call.
const ServiceName = "machinarchy.agent.v1.AgentGateway"

// Defaults applied by New when a field is zero.
const (
	DefaultMaxStreams     = 10000
	DefaultOpenTimeout    = 10 * time.Second
	DefaultAckTimeout     = 30 * time.Second
	DefaultPollInterval   = 2 * time.Second
	DefaultCallsPerSecond = 20
	DefaultCallBurst      = 40
	maxAckBytes           = 128
)

// Config tunes the service. Zero values use the defaults above.
type Config struct {
	Gateway *agentgateway.Gateway
	Logger  *slog.Logger
	// MaxStreams bounds concurrently open Commands streams.
	MaxStreams int
	// OpenTimeout bounds the wait for a stream's opening request.
	OpenTimeout time.Duration
	// AckTimeout is how long a delivered command may stay unacknowledged
	// before it is redelivered.
	AckTimeout time.Duration
	// PollInterval is how often an idle stream re-checks the lease.
	PollInterval time.Duration
	// CallsPerSecond and CallBurst limit unary calls per client address and
	// results per agent.
	CallsPerSecond float64
	CallBurst      int
}

// Server serves the agent gateway over gRPC.
type Server struct {
	cfg    Config
	logger *slog.Logger
	grpc   *grpc.Server

	mu        sync.Mutex
	streams   map[string]*streamRef // agent ID -> its current stream
	limiters  map[string]*rate.Limiter
	openCount int
}

// New builds the service. Serve it with Serve, or mount Handler on the
// controller's TLS endpoint.
func New(cfg Config) (*Server, error) {
	if cfg.Gateway == nil {
		return nil, errors.New("the agent gRPC service requires a gateway")
	}
	if cfg.MaxStreams <= 0 {
		cfg.MaxStreams = DefaultMaxStreams
	}
	if cfg.OpenTimeout <= 0 {
		cfg.OpenTimeout = DefaultOpenTimeout
	}
	if cfg.AckTimeout <= 0 {
		cfg.AckTimeout = DefaultAckTimeout
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.CallsPerSecond <= 0 {
		cfg.CallsPerSecond = DefaultCallsPerSecond
	}
	if cfg.CallBurst <= 0 {
		cfg.CallBurst = DefaultCallBurst
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Server{cfg: cfg, logger: cfg.Logger.With("component", "agent-grpc"),
		streams: map[string]*streamRef{}, limiters: map[string]*rate.Limiter{}}
	s.grpc = grpc.NewServer(
		grpc.ForceServerCodecV2(codec{}),
		grpc.MaxRecvMsgSize(agentv1.MaxResultMessageBytes),
		grpc.MaxConcurrentStreams(1024),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: time.Minute, Timeout: 20 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 20 * time.Second, PermitWithoutStream: true}),
	)
	s.grpc.RegisterService(&serviceDesc, s)
	return s, nil
}

// Handler returns the service as an http.Handler for HTTP/2 over TLS.
func (s *Server) Handler() http.Handler { return s.grpc }

// IsGRPC reports whether r is a gRPC request, so a shared TLS endpoint can
// route it here and everything else to the HTTP API.
func IsGRPC(r *http.Request) bool {
	return r.ProtoMajor == 2 && len(r.Header.Get("Content-Type")) >= len("application/grpc") &&
		r.Header.Get("Content-Type")[:len("application/grpc")] == "application/grpc"
}

// Serve accepts connections on l until Stop. l must already terminate TLS.
func (s *Server) Serve(l net.Listener) error { return s.grpc.Serve(l) }

// Stop closes all connections and streams.
func (s *Server) Stop() { s.grpc.Stop() }

// GracefulStop stops accepting calls and waits for open ones.
func (s *Server) GracefulStop() { s.grpc.GracefulStop() }

func (s *Server) limiter(key string) *rate.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.limiters[key]
	if !ok {
		if len(s.limiters) > 100000 { // bound memory; dropping limiters only forgives
			s.limiters = map[string]*rate.Limiter{}
		}
		l = rate.NewLimiter(rate.Limit(s.cfg.CallsPerSecond), s.cfg.CallBurst)
		s.limiters[key] = l
	}
	return l
}

func clientKey(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		if host, _, err := net.SplitHostPort(p.Addr.String()); err == nil {
			return "ip:" + host
		}
		return "ip:" + p.Addr.String()
	}
	return "ip:unknown"
}

func (s *Server) allowCall(ctx context.Context) error {
	if !s.limiter(clientKey(ctx)).Allow() {
		return status.Error(codes.ResourceExhausted, "too many requests")
	}
	return nil
}

var (
	errInvalid      = status.Error(codes.InvalidArgument, "invalid request")
	errUnauthorized = status.Error(codes.Unauthenticated, "not authorized")
)

// reply answers a request whose handler produced no answer ("nil means send
// nothing") with a generic error, never with detail.
func reply(out []byte) (*frame, error) {
	if out == nil {
		return nil, errInvalid
	}
	return &frame{data: out}, nil
}

func (s *Server) register(ctx context.Context, in *frame) (*frame, error) {
	if err := s.allowCall(ctx); err != nil {
		return nil, err
	}
	return reply(s.cfg.Gateway.Register(ctx, in.data))
}

func (s *Server) renew(ctx context.Context, in *frame) (*frame, error) {
	if err := s.allowCall(ctx); err != nil {
		return nil, err
	}
	return reply(s.cfg.Gateway.Renew(ctx, in.data))
}

func (s *Server) sendResult(ctx context.Context, in *frame) (*frame, error) {
	if err := s.allowCall(ctx); err != nil {
		return nil, err
	}
	err := s.cfg.Gateway.ReceiveResult(ctx, in.data, func(agentID string) bool { return s.limiter("agent:" + agentID).Allow() })
	switch {
	case err == nil:
		return &frame{}, nil
	case errors.Is(err, agentgateway.ErrRateLimited):
		return nil, status.Error(codes.ResourceExhausted, "too many results")
	case errors.Is(err, agentgateway.ErrNotAuthorized):
		return nil, errUnauthorized
	default:
		return nil, status.Error(codes.Unavailable, "result could not be stored")
	}
}

// streamRef identifies one open stream so only its owner frees the slot.
type streamRef struct{ cancel context.CancelFunc }

// admit reserves a stream slot for agentID and replaces the agent's previous
// stream (a reconnect must not be locked out by a half-open old one).
func (s *Server) admit(agentID string, cancel context.CancelFunc) (release func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, replacing := s.streams[agentID]
	if !replacing && s.openCount >= s.cfg.MaxStreams {
		return nil, status.Error(codes.ResourceExhausted, "too many open streams")
	}
	if replacing {
		old.cancel()
	} else {
		s.openCount++
	}
	ref := &streamRef{cancel: cancel}
	s.streams[agentID] = ref
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.streams[agentID] == ref {
				delete(s.streams, agentID)
				s.openCount--
			}
		})
	}, nil
}

func (s *Server) commands(stream grpc.ServerStream) error {
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	if err := s.allowCall(ctx); err != nil {
		return err
	}
	// 1. The first message proves identity and lease.
	first := make(chan *frame, 1)
	go func() {
		var in frame
		if err := stream.RecvMsg(&in); err != nil {
			first <- nil
			return
		}
		first <- &in
	}()
	var open *frame
	select {
	case open = <-first:
	case <-time.After(s.cfg.OpenTimeout):
		return status.Error(codes.DeadlineExceeded, "stream was not opened in time")
	case <-ctx.Done():
		return ctx.Err()
	}
	if open == nil {
		return errInvalid
	}
	agentID, err := s.cfg.Gateway.AuthenticateStream(ctx, open.data)
	if errors.Is(err, agentgateway.ErrUnavailable) {
		return status.Error(codes.Unavailable, "gateway unavailable")
	}
	if err != nil {
		return errUnauthorized
	}
	release, err := s.admit(agentID, cancel)
	if err != nil {
		return err
	}
	defer release()
	s.logger.Info("agent stream opened", "agent_id", agentID)
	defer s.logger.Info("agent stream closed", "agent_id", agentID)

	// 2. Acknowledgements arrive on the same stream.
	acks := make(chan struct{}, 1)
	go func() {
		defer cancel()
		for {
			var in frame
			if err := stream.RecvMsg(&in); err != nil {
				return
			}
			if len(in.data) == 0 || len(in.data) > maxAckBytes {
				return // a malformed acknowledgement ends the stream
			}
			select {
			case acks <- struct{}{}:
			default:
			}
		}
	}()

	// 3. Deliver one command at a time, in queue order, until it is acknowledged.
	for {
		if ctx.Err() != nil {
			return status.FromContextError(ctx.Err()).Err()
		}
		if !s.cfg.Gateway.StreamValid(ctx, agentID) {
			return status.Error(codes.PermissionDenied, "lease revoked or expired")
		}
		delivery, ok, err := s.cfg.Gateway.NextCommand(ctx, agentID, s.cfg.PollInterval)
		if err != nil {
			if ctx.Err() != nil {
				return status.FromContextError(ctx.Err()).Err()
			}
			return status.Error(codes.Unavailable, "command queue unavailable")
		}
		if !ok {
			continue
		}
		select { // drop a stale acknowledgement
		case <-acks:
		default:
		}
		if err := stream.SendMsg(&frame{data: delivery.Payload}); err != nil {
			delivery.Nack()
			return err
		}
		if !s.waitAck(ctx, acks, agentID) {
			delivery.Nack()
			continue
		}
		delivery.Ack()
	}
}

// waitAck waits for the acknowledgement of the delivered command; false means
// it timed out or the stream ended. A revoked agent stops waiting at the next
// poll tick.
func (s *Server) waitAck(ctx context.Context, acks <-chan struct{}, agentID string) bool {
	deadline := time.NewTimer(s.cfg.AckTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(s.cfg.PollInterval)
	defer tick.Stop()
	for {
		select {
		case <-acks:
			return true
		case <-deadline.C:
			return false
		case <-ctx.Done():
			return false
		case <-tick.C:
			if !s.cfg.Gateway.StreamValid(ctx, agentID) {
				return false
			}
		}
	}
}
