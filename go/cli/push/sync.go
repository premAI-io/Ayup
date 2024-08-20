package push

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/trace"

	"go.opentelemetry.io/otel/attribute"
	tr "go.opentelemetry.io/otel/trace"
	"premai.io/Ayup/go/internal/terror"
)

func (s *Pusher) Sync(pctx context.Context) (err error) {
	ctx, span := trace.Span(pctx, "sync")
	defer span.End()

	var wdir string

	if s.SrcDir == "" {
		wdir, err = os.Getwd()
		if err != nil {
			return terror.Errorf(ctx, "getwd: %w", err)
		}
	} else {
		wdir = s.SrcDir
	}

	dfs := os.DirFS(wdir)

	stream, err := s.Client.Sync(ctx)
	if err != nil {
		return terror.Errorf(ctx, "sync stream: %w", err)
	}

	defer func() {
		res, err2 := stream.CloseAndRecv()
		if err2 == nil || err2 == io.EOF {
			if res == nil {
				err2 = terror.Errorf(ctx, "stream close and recv: no response")
			} else if res.Error != nil {
				err = terror.Errorf(ctx, "%s", res.Error.Error)
			}
		} else {
			err2 = terror.Errorf(ctx, "stream close and recv: %w", err2)
		}

		if err == nil {
			err = err2
		}
	}()

	var wg sync.WaitGroup
	defer wg.Wait()

	cancelChan := make(chan struct{})
	logViewProg := tea.NewProgram(NewLogView("sync", cancelChan))
	defer logViewProg.Send(DoneMsg{})

	wg.Add(1)
	go func() {
		defer wg.Done()

		_, err := logViewProg.Run()
		if err != nil {
			terror.Ackf(ctx, "logView run: %w", err)
			cancelChan <- struct{}{}
		}
	}()

	buf := make([]byte, 16*1024)
	chunks := make([]*pb.FileChunk, 0, 32)
	// not including overhead, remember the 4MB grpc limit if playing with the envelope size
	length := 0

	sendFileChunks := func() error {
		if len(chunks) < 1 {
			return nil
		}

		select {
		case <-cancelChan:
			if err := stream.Send(&pb.FileChunks{
				Cancel: true,
			}); err != nil {
				return terror.Errorf(ctx, "stream send: %w", err)
			}

			return terror.Errorf(ctx, "User cancelled")
		default:
		}

		if err := stream.Send(&pb.FileChunks{
			Chunk: chunks,
		}); err != nil {
			return terror.Errorf(ctx, "stream send: %w", err)
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
					return terror.Errorf(ctx, "file read: bytes written is negative: %d", c)
				}

				if err != nil {
					if err == io.EOF {
						last = true
					} else {
						return terror.Errorf(ctx, "file read: %w", err)
					}
				}

				chunkLength += c

				if last || chunkLength+length > 15*1024 {
					break
				}
			}

			chunks = append(chunks, &pb.FileChunk{
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

	err = fs.WalkDir(dfs, ".", func(path string, d fs.DirEntry, err error) error {

		event_attrs := []attribute.KeyValue{
			attribute.String("path", path),
			attribute.Bool("isDir", d.IsDir()),
			attribute.Bool("IsRegular", d.Type().IsRegular()),
		}
		if err != nil {
			return terror.Errorf(ctx, "walkdir func: %w", err)
		}

		info, err := d.Info()
		if err != nil {
			return terror.Errorf(ctx, "dir info: %w", err)
		}

		event_attrs = append(event_attrs,
			attribute.String("mode", info.Mode().String()),
			attribute.Int64("size", info.Size()),
		)

		skipNotice := func(kind string) {
			span.AddEvent("skip", tr.WithAttributes(event_attrs...))
			logViewProg.Send(LogMsg{
				source: "ayup",
				body:   fmt.Sprintf("Skip %s: %s", kind, path),
			})
		}

		if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
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
		logViewProg.Send(LogMsg{
			source: "ayup",
			body:   fmt.Sprintf("Send %d%s: %s", size, unit, path),
		})
		r, err := dfs.Open(path)
		if err != nil {
			return terror.Errorf(ctx, "open read: %w", err)
		}
		defer r.Close()

		return sendFile(path, r)
	})

	if err != nil {
		return
	}

	if err = sendFileChunks(); err != nil {
		return
	}

	return
}
