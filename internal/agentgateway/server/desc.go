package server

import (
	"context"

	"google.golang.org/grpc"
)

type service interface {
	register(context.Context, *frame) (*frame, error)
	renew(context.Context, *frame) (*frame, error)
	sendResult(context.Context, *frame) (*frame, error)
	commands(grpc.ServerStream) error
}

func unary(name string, call func(service, context.Context, *frame) (*frame, error)) grpc.MethodDesc {
	return grpc.MethodDesc{MethodName: name, Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		var in frame
		if err := dec(&in); err != nil {
			return nil, err
		}
		handler := func(ctx context.Context, req any) (any, error) { return call(srv.(service), ctx, req.(*frame)) }
		if interceptor == nil {
			return handler(ctx, &in)
		}
		return interceptor(ctx, &in, &grpc.UnaryServerInfo{Server: srv, FullMethod: "/" + ServiceName + "/" + name}, handler)
	}}
}

var serviceDesc = grpc.ServiceDesc{
	ServiceName: ServiceName,
	HandlerType: (*service)(nil),
	Methods: []grpc.MethodDesc{
		unary("Register", service.register),
		unary("Renew", service.renew),
		unary("SendResult", service.sendResult),
	},
	Streams: []grpc.StreamDesc{{
		StreamName: "Commands", ServerStreams: true, ClientStreams: true,
		Handler: func(srv any, stream grpc.ServerStream) error { return srv.(service).commands(stream) },
	}},
}
