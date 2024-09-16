package push

import (
	"context"
	"io"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/sync/errgroup"
	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/rpc"
	"premai.io/Ayup/go/internal/trace"

	"premai.io/Ayup/go/internal/terror"
)

func (s *Pusher) Download(ctx context.Context) error {
	ctx, span := trace.Span(ctx, "download")
	defer span.End()

	stream, err := s.Client.Download(ctx, &pb.DownloadReq{})
	if err != nil {
		return terror.Errorf(ctx, "client Download: %w", err)
	}
	defer terror.Ackf(ctx, "stream CloseSend: %w", stream.CloseSend())

	retError := func(msg string, args ...any) error {
		return terror.Errorf(ctx, msg, args...)
	}

	ctx, cancelFunc := context.WithCancel(ctx)

	logChan := make(chan string)
	cancelChan := make(chan struct{})
	fileRecver := rpc.NewFileRecver(stream, logChan, retError, retError, s.SrcDir, s.AssistantDir)
	logViewProg := tea.NewProgram(NewLogView("sync", cancelChan))

	var g errgroup.Group

	g.Go(func() error {
		if _, err := logViewProg.Run(); err != nil {
			cancelFunc()
			return terror.Errorf(ctx, "logViewProg Run: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		for {
			select {
			case log, ok := <-logChan:
				if !ok {
					return nil
				}
				logViewProg.Send(LogMsg{
					source: "ayup",
					body:   log,
				})
			case <-cancelChan:
				cancelFunc()
				return nil
			}
		}
	})

	g.Go(func() error {
		err := fileRecver.RecvDirs(ctx)
		logViewProg.Send(DoneMsg{})
		close(logChan)
		if err != nil {
			cancelFunc()
		}
		return err
	})

	return g.Wait()
}

func (s *Pusher) Upload(pctx context.Context) (err error) {
	ctx, span := trace.Span(pctx, "upload")
	defer span.End()

	stream, err := s.Client.Upload(ctx)
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

	logChan := make(chan string)
	defer close(logChan)

	wg.Add(1)
	go func() {
		defer wg.Done()

		for log := range logChan {
			logViewProg.Send(LogMsg{
				source: "ayup",
				body:   log,
			})
		}
	}()

	retError := func(msg string, args ...any) error {
		return terror.Errorf(ctx, msg, args...)
	}

	sender := rpc.NewFileSender(stream, cancelChan, logChan, retError, retError)

	if s.SrcDir != "" {
		if err := sender.SendDir(ctx, pb.Source_app, s.SrcDir); err != nil {
			return err
		}
	}

	if s.AssistantDir == "" {
		return
	}

	return sender.SendDir(ctx, pb.Source_assistant, s.AssistantDir)
}
