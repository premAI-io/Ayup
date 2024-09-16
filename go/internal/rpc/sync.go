package rpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/trace"

	attr "go.opentelemetry.io/otel/attribute"
	tr "go.opentelemetry.io/otel/trace"
	"premai.io/Ayup/go/internal/terror"
)

type fileChunkRecver interface {
	Recv() (*pb.FileChunks, error)
}

type fileRecver struct {
	stream        fileChunkRecver
	logChan       chan string
	sendError     func(string, ...any) error
	internalError func(string, ...any) error
	srcDir        string
	assDir        string

	RecvedAssistant bool
}

func NewFileRecver(
	stream fileChunkRecver,
	logChan chan string,
	sendError func(string, ...any) error,
	internalError func(string, ...any) error,
	srcDir string,
	assDir string,
) fileRecver {
	return fileRecver{
		stream:        stream,
		logChan:       logChan,
		sendError:     sendError,
		internalError: internalError,
		srcDir:        srcDir,
		assDir:        assDir,
	}
}

func (s *fileRecver) RecvDirs(ctx context.Context) error {
	ctx, span := trace.Span(ctx, "RecvDirs")
	defer span.End()

	openFiles := make(map[string]*os.File)
	defer func() {
		for _, file := range openFiles {
			_ = file.Close()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		chunks, err := s.stream.Recv()
		if errors.Is(err, io.EOF) {
			if len(openFiles) > 0 {
				return s.internalError("File stream ended while file chunks are open")
			}
			return nil
		} else if err != nil {
			return s.internalError("stream Recv: %w", err)
		}

		if err != nil {
			return s.internalError("stream recv: %w", err)
		}

		if chunks.Cancel {
			return s.sendError("User cancelled")
		}

		for _, chunk := range chunks.GetChunk() {
			path := chunk.GetPath()
			trace.Event(ctx, "got chunk",
				attr.String("source", chunk.Source.String()),
				attr.String("path", path),
				attr.Bool("last", chunk.Last),
				attr.Int64("offset", chunk.Offset),
				attr.Int("size", len(chunk.Data)),
			)

			if !filepath.IsLocal(chunk.GetPath()) {
				return s.sendError("file path is not local: %s", path)
			}

			base := filepath.Base(path)
			if base == "." || base == "/" {
				return s.sendError("file path has no base name: %s", path)
			}

			dstPath := filepath.Join(s.srcDir, path)
			switch chunk.Source {
			case pb.Source_app:
			case pb.Source_assistant:
				s.RecvedAssistant = true
				dstPath = filepath.Join(s.assDir, path)
			default:
				return s.internalError("unrecognized source: %d", chunk.Source)
			}

			file, alreadyOpen := openFiles[dstPath]
			if !alreadyOpen {
				dir := filepath.Dir(dstPath)

				err := os.MkdirAll(dir, 0700)
				if err != nil {
					return s.internalError("mkdirall: %w", err)
				}

				if s.logChan != nil {
					s.logChan <- fmt.Sprintf("Receiving: %s: %s", chunk.Source.String(), path)
				}
				trace.Event(ctx, "open/create file", attr.String("path", path))
				file, err = os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
				if err != nil {
					return s.internalError("open file: %w", err)
				}

				openFiles[dstPath] = file
			}

			if _, err := file.Write(chunk.Data); err != nil {
				return s.internalError("write file: %w", err)
			}

			if chunk.Last {
				terror.Ackf(ctx, "file close: %w", file.Close())
				delete(openFiles, dstPath)
			}
		}
	}
}

type fileChunkSender interface {
	Send(*pb.FileChunks) error
}

type fileSender struct {
	stream        fileChunkSender
	cancelChan    chan struct{}
	logChan       chan string
	sendError     func(string, ...any) error
	internalError func(string, ...any) error
}

func NewFileSender(
	stream fileChunkSender,
	cancelChan chan struct{},
	logChan chan string,
	sendError func(string, ...any) error,
	internalError func(string, ...any) error,
) fileSender {
	return fileSender{
		stream:        stream,
		cancelChan:    cancelChan,
		logChan:       logChan,
		sendError:     sendError,
		internalError: internalError,
	}
}

func (s fileSender) SendDir(ctx context.Context, source pb.Source, path string) (err error) {
	_, span := trace.Span(ctx, "sync dir", attr.String("path", path))
	defer span.End()

	buf := make([]byte, 16*1024)
	chunks := make([]*pb.FileChunk, 0, 32)
	// not including overhead, remember the 4MB grpc limit if playing with the envelope size
	length := 0

	sendFileChunks := func() error {
		if len(chunks) < 1 {
			return nil
		}

		select {
		case <-s.cancelChan:
			if err := s.stream.Send(&pb.FileChunks{
				Cancel: true,
			}); err != nil {
				return s.internalError("stream send: %w", err)
			}

			return s.sendError("User cancelled")
		default:
		}

		if err := s.stream.Send(&pb.FileChunks{
			Chunk: chunks,
		}); err != nil {
			return s.internalError("stream send: %w", err)
		}

		length = 0
		chunks = make([]*pb.FileChunk, 0, 32)

		return nil
	}

	sendFile := func(path string, r fs.File) error {
		offset := 0

		for {
			if length > 15*1024 || len(chunks) >= 512 {
				if err := sendFileChunks(); err != nil {
					return err
				}
			}

			last := false
			chunkLength := 0

			for {
				data := buf[length+chunkLength:]
				c, err := r.Read(data)

				if c < 0 {
					return s.internalError("file read: bytes written is negative: %d", c)
				}

				if err != nil {
					if err == io.EOF {
						last = true
					} else {
						return s.internalError("file read: %w", err)
					}
				}

				chunkLength += c

				if last || chunkLength+length > 15*1024 {
					break
				}
			}

			chunks = append(chunks, &pb.FileChunk{
				Source: source,
				Path:   path,
				Last:   last,
				Data:   buf[length : length+chunkLength],
				Offset: int64(offset),
			})

			length += chunkLength
			offset += chunkLength

			if last {
				break
			}
		}

		return nil
	}

	dfs := os.DirFS(path)

	err = fs.WalkDir(dfs, ".", func(path string, d fs.DirEntry, err error) error {

		event_attrs := []attr.KeyValue{
			attr.String("path", path),
			attr.Bool("isDir", d.IsDir()),
			attr.Bool("IsRegular", d.Type().IsRegular()),
		}
		if err != nil {
			return s.internalError("walkdir func: %w", err)
		}

		info, err := d.Info()
		if err != nil {
			return s.internalError("dir info: %w", err)
		}

		event_attrs = append(event_attrs,
			attr.String("mode", info.Mode().String()),
			attr.Int64("size", info.Size()),
		)

		skipNotice := func(kind string) {
			span.AddEvent("skip", tr.WithAttributes(event_attrs...))
			if s.logChan != nil {
				s.logChan <- fmt.Sprintf("Skip %s: %s", kind, path)
			}
		}

		if strings.HasPrefix(d.Name(), ".") &&
			d.Name() != "." &&
			d.Name() != ".ayup" {
			skipNotice("hidden")

			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if !d.Type().IsRegular() {
			skipNotice("special file")

			return nil
		}

		// TODO: We should probably transmit empty directories
		if d.IsDir() {
			span.AddEvent("skip", tr.WithAttributes(event_attrs...))
			return nil
		}

		span.AddEvent("copy", tr.WithAttributes(event_attrs...))

		size := info.Size()
		unit := "b"
		if size > 1000 {
			unit = "Kb"
			size /= 1000
		}
		if s.logChan != nil {
			s.logChan <- fmt.Sprintf("Send %s: %d%s: %s", source, size, unit, path)
		}
		r, err := dfs.Open(path)
		if err != nil {
			return s.internalError("open read: %w", err)
		}
		defer r.Close()

		return sendFile(path, r)
	})

	if err = sendFileChunks(); err != nil {
		return
	}

	return
}
