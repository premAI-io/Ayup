package push

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"

	pb "premai.io/Ayup/go/internal/grpc/srv"
)

func (s *Pusher) startPortForwarder(ctx context.Context, wg *sync.WaitGroup) (net.Listener, error) {
	wg.Add(1)
	defer wg.Done()

	listener, err := net.Listen("tcp", "localhost:5000")
	if err != nil {
		return nil, terror.Errorf(ctx, "net listen: %w", err)
	}

	trace.Event(ctx, "TCP proxy listening on 5000")

	egress := func(ctx context.Context, wg *sync.WaitGroup, conn net.Conn, stream pb.Srv_ForwardClient) {
		defer wg.Done()

		buf := make([]byte, 16*1024)
		for {
			len, err := conn.Read(buf)
			if err != nil {
				if err != io.EOF {
					terror.Ackf(ctx, "conn read: %w", err)
				}

				if err := stream.CloseSend(); err != nil {
					terror.Ackf(ctx, "stream close send: %w", err)
				}
				return
			}

			trace.Event(ctx, "egress conn read")

			if err := stream.Send(&pb.ForwardRequest{
				Data: buf[:len],
			}); err != nil {
				terror.Ackf(ctx, "stream send: %w", err)
				return
			}

			trace.Event(ctx, "egress stream send")
		}
	}

	ingress := func(ctx context.Context, wg *sync.WaitGroup, conn net.Conn, stream pb.Srv_ForwardClient) {
		defer wg.Done()

		for {
			res, err := stream.Recv()
			if err != nil {
				if err != io.EOF {
					terror.Ackf(ctx, "stream recv: %w", err)
				}
				break
			}

			if res.Closed {
				break
			}

			trace.Event(ctx, "ingress stream recv")

			if _, err := conn.Write(res.Data); err != nil {
				if !errors.Is(err, net.ErrClosed) {
					terror.Ackf(ctx, "conn write: %w", err)
				}
				return
			}

			trace.Event(ctx, "ingress conn write")
		}

		terror.Ackf(ctx, "conn close: %w", conn.Close())
	}

	handler := func(conn net.Conn) {
		defer func() { terror.Ackf(ctx, "proxy conn close: %w", conn.Close()) }()

		stream, err := s.Client.Forward(ctx)
		if err != nil {
			terror.Ackf(ctx, "client forward: %w", err)
			return
		}

		var wg sync.WaitGroup
		wg.Add(2)

		ctx := stream.Context()

		go ingress(ctx, &wg, conn, stream)
		go egress(ctx, &wg, conn, stream)

		wg.Wait()

		trace.Event(ctx, "conn done")
	}

	go func() {
		conns := make([]net.Conn, 0, 16)
		for {
			conn, err := listener.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					terror.Ackf(ctx, "listener accept: %w", err)
				} else {
					trace.Event(ctx, "listener done")
				}
				break
			}

			trace.Event(ctx, "port forwarder listener accept")

			conns = append(conns, conn)
			go handler(conn)
		}

		for _, conn := range conns {
			_ = conn.Close()
		}
	}()

	return listener, nil
}

func (s *Pusher) RunDocker(ctx context.Context) (err error) {
	ctx, span := trace.Span(ctx, "run")
	defer span.End()

	stream, err := s.Client.Run(ctx)
	if err != nil {
		return terror.Errorf(ctx, "client run: %w", err)
	}
	defer func() {
		err2 := stream.CloseSend()
		if err == nil {
			err = err2
		}
	}()

	cancelChan := make(chan struct{})
	logView := NewLogView("run", cancelChan)
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
		defer wg.Done()

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

	proxy, proxyError := s.startPortForwarder(ctx, &wg)
	defer func() {
		if proxyError == nil {
			terror.Ackf(ctx, "proxy close: %w", proxy.Close())
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
