package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"

	"go.opentelemetry.io/otel/trace"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/lipgloss"
	"github.com/joho/godotenv"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/muesli/termenv"

	"premai.io/Ayup/go/cli/key"
	"premai.io/Ayup/go/cli/login"
	"premai.io/Ayup/go/cli/push"
	"premai.io/Ayup/go/internal/terror"
	ayTrace "premai.io/Ayup/go/internal/trace"
	"premai.io/Ayup/go/internal/tui"
	"premai.io/Ayup/go/srv"
)

//go:embed version.txt
var version string

type Globals struct {
	Ctx    context.Context
	Tracer trace.Tracer
	Logger *slog.Logger
}

type PushCmd struct {
	Path string `arg:"" optional:"" name:"path" help:"Path to the source code to be pushed" type:"path"`

	Host string `env:"AYUP_PUSH_HOST" default:"localhost:50051" help:"The location of a service we can push to"`
}

func (s *PushCmd) Run(g Globals) (err error) {
	pprof.Do(g.Ctx, pprof.Labels("command", "push"), func(ctx context.Context) {
		p := push.Pusher{
			Tracer:     g.Tracer,
			Host:       s.Host,
			P2pPrivKey: cli.P2pPrivKey,
			SrcDir:     s.Path,
		}

		err = p.Run(pprof.WithLabels(g.Ctx, pprof.Labels("command", "push")))
	})

	return
}

type DaemonStartCmd struct {
	Host           string `env:"AYUP_DAEMON_HOST" default:":50051" help:"The addresses and port to listen on"`
	ContainerdAddr string `env:"AYUP_CONTAINERD_ADDR" help:"The path to the containerd socket if not using Docker's" default:"/var/run/docker/containerd/containerd.sock"`
	BuildkitdAddr  string `env:"AYUP_BUILDKITD_ADDR" help:"The path to the buildkitd socket if not the default" default:"unix:///run/buildkit/buildkitd.sock"`

	P2pAuthorizedClients string `env:"AYUP_P2P_AUTHORIZED_CLIENTS" help:"Comma deliminated public keys of logged in clients"`
}

func (s *DaemonStartCmd) Run(g Globals) (err error) {
	pprof.Do(g.Ctx, pprof.Labels("command", "deamon start"), func(ctx context.Context) {
		var tmp string
		tmp, err = os.MkdirTemp("", "ayup-*")
		if err != nil {
			err = terror.Errorf(g.Ctx, "MkdirTemp: %w", err)
			return
		}

		r := srv.Srv{
			TmpDir:     tmp,
			SrcDir:     filepath.Join(tmp, "src"),
			ImgTarPath: filepath.Join(tmp, "image.tar"),
			ImgName:    "docker.io/richardprem/ayup:test",

			Host:           s.Host,
			ContainerdAddr: s.ContainerdAddr,
			BuildkitdAddr:  s.BuildkitdAddr,
			P2pPrivKey:     cli.P2pPrivKey,
		}

		var authedClients []peer.ID
		if s.P2pAuthorizedClients != "" {
			for _, peerStr := range strings.Split(s.P2pAuthorizedClients, ",") {

				var peerId peer.ID
				peerId, err = peer.Decode(peerStr)
				if err != nil {
					err = terror.Errorf(g.Ctx, "Error while parsing authorized client: `%s`: peer Decode: %w", peerStr, err)
					return
				}

				authedClients = append(authedClients, peerId)
			}
		}
		r.P2pAuthedClients = authedClients

		err = r.RunServer(ctx)
	})

	return
}

type LoginCmd struct {
	Host string `arg:"" env:"AYUP_LOGIN_HOST" help:"The server's P2P multi-address including the peer ID e.g. /dns4/example.com/50051/p2p/1..."`
}

func (s *LoginCmd) Run(g Globals) error {
	l := login.Login{
		Host:       s.Host,
		P2pPrivKey: cli.P2pPrivKey,
	}

	return l.Run(g.Ctx)
}

type KeyNewCmd struct{}

func (s *KeyNewCmd) Run(g Globals) error {
	return key.New(g.Ctx)
}

var cli struct {
	Push  PushCmd  `cmd:"" help:"Figure out how to deploy your application"`
	Login LoginCmd `cmd:"" help:"Login to the Ayup service" hidden:""`

	Daemon struct {
		Start DaemonStartCmd `cmd:"" help:"Start an Ayup service Daemon"`
	} `cmd:"" help:"Self host Ayup"`

	Key struct {
		New KeyNewCmd `cmd:"" help:"Create a new private key"`
	} `cmd:"" help:"Manage encryption keys used by Ayup"`

	P2pPrivKey string `env:"AYUP_P2P_PRIV_KEY" help:"Secret encryption key produced by 'ay key new'"`

	// maybe effected by https://github.com/open-telemetry/opentelemetry-go/issues/5562
	// also https://github.com/moby/moby/issues/46129#issuecomment-2016552967
	TelemetryEndpoint string `group:"monitoring" env:"OTEL_EXPORTER_OTLP_ENDPOINT" help:"the host that telemetry data is sent to; e.g. localhost:4317"`
	ProfilingEndpoint string `group:"monitoring" env:"PYROSCOPE_ADHOC_SERVER_ADDRESS" help:"URL performance data is sent to; e.g. http://localhost:4040"`
}

func main() {
	ctx := context.Background()

	// Disable dynamic dark background detection
	// https://github.com/charmbracelet/lipgloss/issues/73
	lipgloss.SetHasDarkBackground(termenv.HasDarkBackground())
	titleStyle := tui.TitleStyle
	versionStyle := tui.VersionStyle
	errorStyle := tui.ErrorStyle
	fmt.Print(titleStyle.Render("Ayup!"), " ", versionStyle.Render("v"+version), "\n\n")

	confDir, userConfDirErr := os.UserConfigDir()
	var godotenvLoadErr error
	if userConfDirErr == nil {
		godotenvLoadErr = godotenv.Load(filepath.Join(confDir, "ayup", "env"))
	}

	ktx := kong.Parse(&cli, kong.UsageOnError(), kong.Description("Just make it run!"))

	ayTrace.SetupPyroscopeProfiling(cli.ProfilingEndpoint)

	stopTracing, err := ayTrace.SetupOTelSDK(ctx, cli.TelemetryEndpoint)
	if err != nil {
		log.Fatalln(err)
	}
	defer func() {
		if err := stopTracing(ctx); err != nil {
			log.Fatalln(err)
		}
	}()

	tracer := otel.Tracer("premai.io/Ayup/go/internal/trace")
	logger := otelslog.NewLogger("premai.io/Ayup/go/internal/trace")

	ctx = ayTrace.SetSpanKind(ctx, trace.SpanKindClient)
	ctx, span := tracer.Start(ctx, "main")
	defer span.End()

	terror.Ackf(ctx, "os UserConfigDir: %w", userConfDirErr)
	terror.Ackf(ctx, "godotenv load: %w", godotenvLoadErr)

	err = ktx.Run(Globals{
		Ctx:    ctx,
		Tracer: tracer,
		Logger: logger,
	})

	if err == nil {
		return
	}

	fmt.Println(errorStyle.Render("Error!"), err)

	var perr *kong.ParseError
	if errors.As(err, &perr) {
		if err := ktx.PrintUsage(false); err != nil {
			_ = terror.Errorf(ctx, "printusage: %w", err)
		}
	}
}
