package server

import (
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/mem"
)

// CodecName is the gRPC content subtype of the agent service.
const CodecName = "agentv1"

// frame is one opaque message: a signed agentv1 envelope, or for stream
// acknowledgements the command ID. The service never parses anything else
// from the transport.
type frame struct{ data []byte }

// codec carries frames as raw bytes (no protobuf schema).
type codec struct{}

var _ encoding.CodecV2 = codec{}

func (codec) Name() string { return CodecName }

func (codec) Marshal(v any) (mem.BufferSlice, error) {
	f, ok := v.(*frame)
	if !ok {
		return nil, fmt.Errorf("agent codec cannot marshal %T", v)
	}
	return mem.BufferSlice{mem.SliceBuffer(f.data)}, nil
}

func (codec) Unmarshal(data mem.BufferSlice, v any) error {
	f, ok := v.(*frame)
	if !ok {
		return fmt.Errorf("agent codec cannot unmarshal into %T", v)
	}
	f.data = data.Materialize()
	return nil
}

// Codec is the client-side call option for the agent service (used by the
// test client here and mirrored by the agent's transport).
func Codec() grpc.CallOption { return grpc.ForceCodecV2(codec{}) }

// Frame is the exported wire message for clients.
type Frame = frame

// NewFrame wraps bytes for a client call.
func NewFrame(b []byte) *Frame { return &frame{data: b} }

// Bytes returns the frame content.
func (f *frame) Bytes() []byte { return f.data }
