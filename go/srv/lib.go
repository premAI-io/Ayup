package srv

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/charmbracelet/lipgloss"

	gostream "github.com/libp2p/go-libp2p-gostream"
	p2pPeer "github.com/libp2p/go-libp2p/core/peer"

	"google.golang.org/grpc"
	"google.golang.org/grpc/peer"

	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/rpc"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	attr "go.opentelemetry.io/otel/attribute"
	tr "go.opentelemetry.io/otel/trace"
)

type Push struct {
	analysis *pb.AnalysisResult
}

type Srv struct {
	pb.UnimplementedSrvServer

	TmpDir     string
	SrcDir     string
	ImgTarPath string
	ImgName    string

	Host             string
	P2pPrivKey       string
	P2pAuthedClients []p2pPeer.ID

	ContainerdAddr string
	BuildkitdAddr  string

	// Instance of a push, here while we don't have apps, users, sessions etc.
	push Push

	tuiMutex sync.Mutex
}

func newErrorReply(error string) *pb.ActReply {
	return &pb.ActReply{
		Source: "ayup",
		Variant: &pb.ActReply_Error{
			Error: &pb.Error{
				Error: error,
			},
		},
	}
}

func mkSendError(ctx context.Context, stream ActServer) func(string, ...any) error {
	return func(msgf string, args ...any) error {
		oerr := terror.Errorf(ctx, msgf, args...)
		err := stream.Send(newErrorReply(oerr.Error()))
		if err != nil {
			_ = terror.Errorf(ctx, "stream send: %w", err)
		}
		return nil
	}
}

func mkInternalError(ctx context.Context, stream ActServer) func(string, ...any) error {
	span := tr.SpanFromContext(ctx)
	sendError := mkSendError(ctx, stream)

	return func(msgf string, args ...any) error {
		_ = terror.Errorf(ctx, msgf, args...)
		return sendError(fmt.Sprintf("Internal Error: Support ID: %s", span.SpanContext().SpanID()))
	}
}

type recvReq struct {
	req *pb.ActReq
	err error
}

func mkRecvChan(ctx context.Context, stream ActServer) chan recvReq {
	c := make(chan recvReq)

	go func(ctx context.Context) {
		for {
			req, err := stream.Recv()
			if err != nil && err != io.EOF {
				err = terror.Errorf(ctx, "stream recv: %w", err)
			}

			c <- recvReq{req, err}

			if err == io.EOF {
				break
			}
		}
	}(ctx)

	return c
}

type procOut struct {
	stdio string
	err   error
	ret   *int
}

type procIn struct {
	cancel bool
	stdio  []byte
}

func startProc(ctx context.Context, cmd *exec.Cmd) (chan<- procIn, <-chan procOut) {
	span := tr.SpanFromContext(ctx)
	procInChan := make(chan procIn, 1)
	procOutChan := make(chan procOut, 1)

	errOut := func(err error) (chan<- procIn, <-chan procOut) {
		procOutChan <- procOut{
			err: err,
		}

		close(procOutChan)

		return procInChan, procOutChan
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return errOut(terror.Errorf(ctx, "stdin pipe: %w", err))
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return errOut(terror.Errorf(ctx, "stdout pipe: %w", err))
	}
	outreader := bufio.NewReader(stdout)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return errOut(terror.Errorf(ctx, "stderr pipe: %w", err))
	}
	errreader := bufio.NewReader(stderr)

	var wg sync.WaitGroup
	wg.Add(2)

	readOut := func(ctx context.Context, reader *bufio.Reader) {
		defer wg.Done()

		scanner := bufio.NewScanner(reader)

		for scanner.Scan() {
			text := scanner.Text()
			span.AddEvent("log", tr.WithAttributes(attr.String("text", text)))

			if text != "" {
				procOutChan <- procOut{
					stdio: scanner.Text(),
				}
			}
		}
	}

	go readOut(ctx, outreader)
	go readOut(ctx, errreader)

	err = cmd.Start()
	if err != nil {
		return errOut(terror.Errorf(ctx, "cmd start: %w", err))
	}

	procDone := make(chan struct{})

	go func() {
		defer close(procOutChan)

		go func(ctx context.Context) {
			err = cmd.Wait()
			exitCode := 0
			if err != nil {
				if exitError, ok := err.(*exec.ExitError); ok {
					exitCode = exitError.ExitCode()
				}
				err = terror.Errorf(ctx, "cmd wait: %w", err)
			}

			// send the logs before the exit code
			wg.Wait()
			procOutChan <- procOut{
				err: err,
				ret: &exitCode,
			}
			close(procDone)
		}(ctx)

		termProc := func() {
			if cmd.Process == nil {
				return
			}

			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				_ = terror.Errorf(ctx, "signal interrupt: %w", err)
			}
		}

		span.AddEvent("Started proc")
	loop:
		for {
			select {
			case r := <-procInChan:
				if r.cancel {
					termProc()
				} else if r.stdio != nil {
					if _, err := stdin.Write(r.stdio); err != nil {
						procOutChan <- procOut{
							err: terror.Errorf(ctx, "stdin write: %w", err),
						}
						termProc()
					}

					if err := stdin.Close(); err != nil {
						procOutChan <- procOut{
							err: terror.Errorf(ctx, "stdin close: %w", err),
						}
						termProc()
					}
				}
			case <-ctx.Done():
				termProc()
			case <-procDone:
				break loop
			}
		}
	}()

	return procInChan, procOutChan
}

type procWaiterFn func(string, chan<- procIn, <-chan procOut) error

func mkProcWaiter(ctx context.Context, stream ActServer, recvChan chan recvReq) procWaiterFn {
	sendError := mkSendError(ctx, stream)
	internalError := mkInternalError(ctx, stream)
	sendLog := func(source string, text string) error {
		return stream.Send(&pb.ActReply{
			Source: source,
			Variant: &pb.ActReply_Log{
				Log: text,
			},
		})
	}

	return func(name string, in chan<- procIn, out <-chan procOut) (err error) {
		for {
			select {
			case r := <-recvChan:
				in <- procIn{cancel: true}

				if r.err != nil && r.err == io.EOF {
					err = stream.Send(newErrorReply(r.err.Error()))
				} else if r.req.GetCancel() {
					err = sendLog("ayup", "User cancelled")
				} else {
					err = sendError("Unexpected request: %v", r.req)
				}
			case e := <-out:
				if e.stdio != "" {
					if err = sendLog(name, e.stdio); err == nil {
						continue
					}
				}

				if e.ret != nil {
					if *e.ret > 0 {
						return sendError("%s returned: %d", name, *e.ret)
					} else {
						err = stream.Send(&pb.ActReply{})
					}
				}

				if e.err != nil {
					err = internalError("%s: %w", name, e.err)
				}

				return err
			}
		}
	}
}

func remotePeerId(ctx context.Context) (peerId p2pPeer.ID, err error) {
	pr, ok := peer.FromContext(ctx)
	if !ok {
		return peerId, fmt.Errorf("failed to get peer info from context")
	}

	peerIdStr := pr.Addr.String()
	peerId, err = p2pPeer.Decode(peerIdStr)
	if err != nil {
		return peerId, terror.Errorf(ctx, "peer Decode: %w", err)
	}

	return
}

func (s *Srv) checkPeerAuth(ctx context.Context) (bool, error) {
	span := tr.SpanFromContext(ctx)

	pr, ok := peer.FromContext(ctx)
	if !ok {
		return false, fmt.Errorf("failed to get peer info from context")
	}

	if pr.Addr.Network() != gostream.Network {
		trace.Event(ctx, "Authorized due to insecure transport")

		return true, nil
	}

	peerIdStr := pr.Addr.String()
	span.SetAttributes(attr.String("peerId", peerIdStr))

	peerId, err := p2pPeer.Decode(peerIdStr)
	if err != nil {
		return false, terror.Errorf(ctx, "peer Decode: %w", err)
	}

	for _, authedId := range s.P2pAuthedClients {
		if peerId == authedId {
			trace.Event(ctx, "Authorized due to ID match")
			return true, nil
		} else {
			trace.Event(ctx, "No match", attr.String("authedId", authedId.String()))
		}
	}

	trace.Event(ctx, "No authorized peer IDs match")

	return false, nil
}

func (s *Srv) RunServer(pctx context.Context) (err error) {
	ctx := trace.SetSpanKind(pctx, tr.SpanKindServer)
	ctx, span := trace.Span(ctx, "start srv")
	defer span.End()

	privKey, err := rpc.EnsurePrivKey(ctx, s.P2pPrivKey)
	if err != nil {
		return err
	}

	titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("098")).Bold(true)
	if err != nil {
		return terror.Errorf(ctx, "peer IDFromPublicKey: %w", err)
	}

	lis, host, err := rpc.Listen(ctx, s.Host, privKey)
	if err != nil {
		return terror.Errorf(ctx, "listen: %w", err)
	}

	if host != nil {
		for _, maddr := range host.Addrs() {
			peerMaddr := fmt.Sprintf("%s/p2p/%s", maddr.String(), host.ID().String())
			fmt.Println(titleStyle.Render("Connect with:"), fmt.Sprintf("ay login %s", peerMaddr))
		}

		if len(s.P2pAuthedClients) > 0 {
			fmt.Println()
			fmt.Println(titleStyle.Render("Authorized clients:"))
			for _, clientPeerId := range s.P2pAuthedClients {
				fmt.Println("\t", clientPeerId)
			}
		}
	}

	srv := grpc.NewServer(
		grpc.StatsHandler(
			otelgrpc.NewServerHandler(
				otelgrpc.WithTracerProvider(span.TracerProvider()),
				otelgrpc.WithMessageEvents(otelgrpc.ReceivedEvents, otelgrpc.SentEvents),
			),
		),
	)
	pb.RegisterSrvServer(srv, s)
	span.AddEvent("Listening")
	span.End()

	if err := srv.Serve(lis); err != nil {
		return terror.Errorf(ctx, "serve: %w", err)
	}

	return nil
}
