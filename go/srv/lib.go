package srv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	gostream "github.com/libp2p/go-libp2p-gostream"
	p2pPeer "github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/sync/errgroup"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"

	"premai.io/Ayup/go/assistants"
	"premai.io/Ayup/go/internal/assist"
	"premai.io/Ayup/go/internal/conf"
	inrPb "premai.io/Ayup/go/internal/grpc/inrootless"
	pb "premai.io/Ayup/go/internal/grpc/srv"

	"premai.io/Ayup/go/internal/proc"
	"premai.io/Ayup/go/internal/rpc"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
	"premai.io/Ayup/go/internal/tui"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	attr "go.opentelemetry.io/otel/attribute"
	tr "go.opentelemetry.io/otel/trace"
)

type Push struct {
	hasAssistant bool
}

type Srv struct {
	pb.UnimplementedSrvServer

	AssistantDir        string
	RemoteAssistantsDir string
	LocalAssistantsDir  string
	AppDir              string
	StateDir            string
	ScratchDir          string

	Host             string
	P2pPrivKey       string
	P2pAuthedClients []p2pPeer.ID

	BuildkitdAddr string

	registry  *assistants.Registry
	inrClient inrPb.InRootlessClient

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

// TODO: Could be better handled in upstream change to buildkit by setting the cache path
func fixupCni(ctx context.Context) (string, error) {
	if _, err := os.Stat("/var/lib/cni"); err != nil {
		if os.IsNotExist(err) {
			return "/var/lib", nil
		}

		return "", terror.Errorf(ctx, "os Stat(%s): %w", "/var/lib/cni", err)
	}

	return "/var/lib/cni", nil
}

func (s *Srv) runRootlessBuildkit(g *errgroup.Group, ctx context.Context, selfExe string, cniVarPath string) (tr.Span, chan<- proc.In, <-chan proc.Out) {
	ctx, span := trace.Span(ctx, "start buildkit")

	s.BuildkitdAddr = "unix://" + filepath.Join(conf.UserRuntimeDir(), "buildkit", "buildkit.sock")

	cmdArgs := []string{
		"--port-driver=builtin",
		"--net=slirp4netns",
		"--copy-up=/etc",
		"--copy-up=" + cniVarPath,
		"--disable-host-loopback",
		"--detach-netns",

		selfExe, "daemon", "start-in-rootless",

		"--debug",
		"--rootless",
		"--oci-worker-rootless=true",
		"--oci-worker-net=bridge",
		"--containerd-worker=false",
		"--config", filepath.Join(conf.UserConfigDir(), "buildkit", "buildkitd.toml"),
		"--root", filepath.Join(conf.UserRoot(), "buildkit"),
		"--addr", s.BuildkitdAddr,
	}

	cmd := exec.Command("rootlesskit", cmdArgs...)

	pi, po := proc.Start(g, ctx, cmd)

	return span, pi, po
}

func (s *Srv) RunServer(pctx context.Context) (err error) {
	ctx := trace.SetSpanKind(pctx, tr.SpanKindServer)
	ctx, span := trace.Span(ctx, "start srv")
	defer span.End()

	cniVarPath, err := fixupCni(ctx)
	if err != nil {
		return err
	}

	ctx, stopSigFunc := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSigFunc()

	selfExe, err := os.Executable()
	if err != nil {
		return terror.Errorf(ctx, "os Executable: %w", err)
	}

	privKey, err := rpc.EnsurePrivKey(ctx, "AYUP_SERVER_P2P_PRIV_KEY", s.P2pPrivKey)
	if err != nil {
		return err
	}

	s.registry = assistants.NewRegistry()
	if err := s.registry.RegisterDirs(ctx, assist.Remote, s.RemoteAssistantsDir); err != nil {
		return err
	}

	if err := s.registry.RegisterDirs(ctx, assist.Local, s.LocalAssistantsDir); err != nil {
		return err
	}

	titleStyle := tui.TitleStyle
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

	inrConn, err := grpc.NewClient("unix://"+conf.InrootlessAddr(),
		grpc.WithStatsHandler(
			otelgrpc.NewClientHandler(
				otelgrpc.WithTracerProvider(span.TracerProvider()),
				otelgrpc.WithMessageEvents(otelgrpc.ReceivedEvents, otelgrpc.SentEvents),
			),
		),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return terror.Errorf(ctx, "grpc NewClient: %w", err)
	}

	s.inrClient = inrPb.NewInRootlessClient(inrConn)

	var g errgroup.Group

	buildkitSpan, _, buildkitOut := s.runRootlessBuildkit(&g, ctx, selfExe, cniVarPath)

	var wg sync.WaitGroup
	defer wg.Wait()

	wg.Add(1)
	go func() {
		defer wg.Done()

		for pout := range buildkitOut {
			if pout.Err != nil {
				s.tuiMutex.Lock()
				fmt.Println(tui.ErrorStyle.Render("Buildkitd Error!"), pout.Err)
				s.tuiMutex.Unlock()
			}
		}

		buildkitSpan.End()
		stopSigFunc()
	}()

	if _, err := s.inrClient.Ping(ctx, &inrPb.PingRequest{}, grpc.WaitForReady(true)); err != nil {
		return terror.Errorf(ctx, "inrClient Ping: %w", err)
	}

	go func() {
		<-ctx.Done()

		srv.GracefulStop()
	}()

	if err := srv.Serve(lis); err != nil {
		stopSigFunc()
		return terror.Errorf(ctx, "serve: %w", err)
	}

	return g.Wait()
}
