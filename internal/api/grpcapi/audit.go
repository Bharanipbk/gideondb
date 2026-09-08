package grpcapi

import (
	"context"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func auditUnaryInterceptor(recorder AuditRecorder) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (response any, err error) {
		started := time.Now()
		defer func() {
			if recorder != nil {
				recorder.RecordGRPCAudit(info.FullMethod, auditStatus(err), time.Since(started))
			}
		}()
		return handler(ctx, request)
	}
}

func auditStreamInterceptor(recorder AuditRecorder) grpc.StreamServerInterceptor {
	return func(server any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		started := time.Now()
		defer func() {
			if recorder != nil {
				recorder.RecordGRPCAudit(info.FullMethod, auditStatus(err), time.Since(started))
			}
		}()
		return handler(server, stream)
	}
}

func auditStatus(err error) int {
	switch status.Code(err) {
	case codes.OK:
		return http.StatusOK
	case codes.InvalidArgument, codes.OutOfRange:
		return http.StatusBadRequest
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists, codes.Aborted, codes.FailedPrecondition:
		return http.StatusConflict
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.Canceled:
		return 499
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	case codes.Unimplemented:
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}
