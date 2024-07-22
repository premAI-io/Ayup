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

	"go.opentelemetry.io/otel/trace"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"premai.io/Ayup/go/cli/login"
	"premai.io/Ayup/go/cli/push"
	"premai.io/Ayup/go/internal/terror"
	ayTrace "premai.io/Ayup/go/internal/trace"
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
			Tracer: g.Tracer,
			Host:   s.Host,
			SrcDir: s.Path,
		}

		err = p.Run(pprof.WithLabels(g.Ctx, pprof.Labels("command", "push")))
	})

	return
}

type DaemonStartCmd struct {
	ContainerdAddr string `env:"AYUP_CONTAINERD_ADDR" help:"The path to the containerd socket if not using Docker's" default:"/var/run/docker/containerd/containerd.sock"`
	BuildkitdAddr string `env:"AYUP_BUILDKITD_ADDR" help:"The path to the buildkitd socket if not the default" default:"unix:///run/buildkit/buildkitd.sock"`
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

			ContainerdAddr: s.ContainerdAddr,
			BuildkitdAddr: s.BuildkitdAddr,
		}

		err = r.RunServer(ctx)
	})

	return
}

type LoginCmd struct {
	Host string `env:"AYUP_LOGIN_HOST" default:"localhost:50051" help:""`
}

func (s *LoginCmd) Run(g Globals) error {
	l := login.Login{
		Host: s.Host,
	}

	return l.Run(g.Ctx)
}

var cli struct {
	Push  PushCmd  `cmd:"" help:"Figure out how to deploy your application"`
	Login LoginCmd `cmd:"" help:"Login to the Ayup service"`

	Daemon struct {
		Start DaemonStartCmd `cmd:"" help:"Start an Ayup service Daemon"`

	} `cmd:"" help:"Self host Ayup"`

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
	titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("098")).Bold(true)
	versionStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("060"))
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	fmt.Print(titleStyle.Render("Ayup!"), " ", versionStyle.Render("v"+version), "\n\n")

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
