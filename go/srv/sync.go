package srv

import (
	"errors"
	"io"
	"os"

	attr "go.opentelemetry.io/otel/attribute"

	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/rpc"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
)

func (s *Srv) Download(req *pb.DownloadReq, stream pb.Srv_DownloadServer) error {
	ctx := stream.Context()

	sendError := func(msgf string, args ...any) error {
		return terror.Errorf(ctx, msgf, args...)
	}

	internalError := func(msgf string, args ...any) error {
		_ = terror.Errorf(ctx, msgf, args...)
		return sendError("internal error")
	}

	fileSender := rpc.NewFileSender(stream, nil, nil, sendError, internalError)

	if err := fileSender.SendDir(ctx, pb.Source_app, s.AppDir); err != nil {
		return err
	}

	return nil
}

func (s *Srv) Upload(stream pb.Srv_UploadServer) error {
	ctx := stream.Context()
	ctx, span := trace.Span(ctx, "upload", attr.String("srcDir", s.AppDir), attr.String("assDir", s.AssistantDir))
	defer span.End()

	sendErrorClose := func(msgf string, args ...any) error {
		oerr := terror.Errorf(ctx, msgf, args...)
		err := stream.SendAndClose(&pb.Result{
			Error: &pb.Error{Error: oerr.Error()},
		})
		if err != nil {
			_ = terror.Errorf(ctx, "stream send and close: %w", err)
		}
		return nil
	}

	internalError := func(msgf string, args ...any) error {
		_ = terror.Errorf(ctx, msgf, args...)
		return sendErrorClose("internal error")
	}

	if ok, err := s.checkPeerAuth(ctx); !ok || err != nil {
		if err != nil {
			return internalError("checkPeerAuth: %w", err)
		}

		return sendErrorClose("Not authorized")
	}

	if _, err := os.Stat(s.AssistantDir); err == nil {
		if err := os.RemoveAll(s.AssistantDir); err != nil {
			return internalError("RemoveAll: %w", err)
		}
	}

	if err := os.RemoveAll(s.AppDir); err != nil && !os.IsNotExist(err) {
		return internalError("RemoveAll: %w", err)
	}

	if err := os.MkdirAll(s.AppDir, 0700); err != nil {
		return internalError("os MkdirAll: %w", err)
	}

	fileRecvr := rpc.NewFileRecver(stream, nil, sendErrorClose, internalError, s.AppDir, s.AssistantDir)

	if err := fileRecvr.RecvDirs(ctx); err != nil {
		if !errors.Is(err, io.EOF) {
			return err
		}
	}

	if err := stream.SendAndClose(&pb.Result{}); err != nil {
		return internalError("stream send and close: %w", err)
	}

	s.push.hasAssistant = fileRecvr.RecvedAssistant

	return nil
}
