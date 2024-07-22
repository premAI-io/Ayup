package push

import (
	"context"
	"fmt"
	"io"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"

	pb "premai.io/Ayup/go/internal/grpc/srv"
)

func (s *Pusher) Build(ctx context.Context) (err error) {
	ctx, span := trace.Span(ctx, "build")
	defer span.End()

	stream, err := s.Client.Build(ctx)
	if err != nil {
		return terror.Errorf(ctx, "client build: %w", err)
	}
	defer func() {
		err2 := stream.CloseSend()
		if err == nil {
			err = err2
		}
	}()

	cancelChan := make(chan struct{})
	logView := NewLogView("build", cancelChan)

	var wg sync.WaitGroup
	defer wg.Wait()

	logViewProg := tea.NewProgram(logView, tea.WithContext(ctx))
	wg.Add(1)
	go func() {
		defer wg.Done()

		_, err := logViewProg.Run()
		if err != nil {
			terror.Ackf(ctx, "logView run: %w", err)
			cancelChan <- struct{}{}
		}
		close(cancelChan)
	}()

	recvChan := make(chan *pb.ActReply)

	wg.Add(1)
	go func() {
		wg.Done()

		for {
			res, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					span.AddEvent("stream recv EOF")
					return
				}
				terror.Ackf(ctx, "stream recv: %w", err)
				return
			}

			span.AddEvent(fmt.Sprintf("stream recv: %v", res))
			recvChan <- res
		}
	}()

loop:
	for {
		select {
		case _, ok := <-cancelChan:
			if !ok {
				span.AddEvent("done")
				break loop
			}

			span.AddEvent("cancel")
			err = stream.Send(&pb.ActReq{
				Cancel: true,
			})
			if err != nil {
				logViewProg.Send(tea.QuitMsg{})

				err = terror.Errorf(ctx, "stream send: %w", err)
				break loop
			}
		case r := <-recvChan:
			if l := r.GetLog(); l != "" {
				logViewProg.Send(LogMsg{
					source: r.GetSource(),
					body:   l,
				})
				continue
			}

			if c := r.GetChoice(); c != nil {
				logViewProg.Send(tea.QuitMsg{})

				err = terror.Errorf(ctx, "unexpected choice msg")
			}

			if e := r.GetError(); e != nil {
				logViewProg.Send(LogMsg{
					source: r.GetSource(),
					body:   e.Error,
				})

				err = terror.Errorf(ctx, "remote error: %s", e.Error)
			}

			logViewProg.Send(DoneMsg{})
			break loop
		}
	}

	if err := stream.CloseSend(); err != nil {
		terror.Ackf(ctx, "stream closeSend: %w", err)
	}

	return
}
