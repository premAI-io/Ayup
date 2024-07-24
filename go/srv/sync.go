package srv

import (
	"io"
	"os"
	"path/filepath"

	tr "go.opentelemetry.io/otel/trace"

	attr "go.opentelemetry.io/otel/attribute"
	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
)

func (s *Srv) Sync(stream pb.Srv_SyncServer) error {
	pctx := stream.Context()
	ctx := trace.SetSpanKind(pctx, tr.SpanKindServer)
	ctx, span := trace.Span(ctx, "sync", attr.String("srcDir", s.SrcDir))
	defer span.End()

	openFiles := make(map[string]*os.File)
	defer func() {
		for _, file := range openFiles {
			_ = file.Close()
		}
	}()

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

	if _, err := os.Stat(s.SrcDir); err == nil {
		if err := os.RemoveAll(s.SrcDir); err != nil {
			return internalError("RemoveAll: %w", err)
		}
	}

	for {
		chunks, err := stream.Recv()

		if err == io.EOF {
			if err = stream.SendAndClose(&pb.Result{}); err != nil {
				return internalError("stream send and close: %w", err)
			}
			break
		}

		if err != nil {
			return internalError("stream recv: %w", err)
		}

		if chunks.Cancel {
			return sendErrorClose("User cancelled")
		}

		for _, chunk := range chunks.GetChunk() {
			path := chunk.GetPath()
			trace.Event(ctx, "got chunk",
				attr.String("path", path),
				attr.Bool("last", chunk.Last),
				attr.Int64("offset", chunk.Offset),
				attr.Int("size", len(chunk.Data)),
			)

			if !filepath.IsLocal(chunk.GetPath()) {
				return sendErrorClose("file path is not local: %s", path)
			}

			base := filepath.Base(path)
			if base == "." || base == "/" {
				return sendErrorClose("file path has no base name: %s", path)
			}

			dstPath := filepath.Join(s.SrcDir, path)

			file, alreadyOpen := openFiles[dstPath]
			if !alreadyOpen {
				dir := filepath.Dir(dstPath)

				err := os.MkdirAll(dir, 0700)
				if err != nil {
					return internalError("mkdirall: %w", err)
				}

				trace.Event(ctx, "open/create file", attr.String("path", path))
				file, err = os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
				if err != nil {
					return internalError("open file: %w", err)
				}

				openFiles[dstPath] = file
			}

			if _, err := file.Write(chunk.Data); err != nil {
				return internalError("write file: %w", err)
			}

			if chunk.Last {
				terror.Ackf(ctx, "file close: %w", file.Close())
				delete(openFiles, dstPath)
			}
		}
	}

	if len(openFiles) > 0 {
		return sendErrorClose("File stream ended while file chunks are open")
	}

	return nil
}
