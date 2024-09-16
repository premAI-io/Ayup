package ay

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
	"github.com/joho/godotenv"
	"github.com/muesli/termenv"

	"premai.io/Ayup/go/cli/assistants"
	"premai.io/Ayup/go/cli/key"
	"premai.io/Ayup/go/cli/login"
	"premai.io/Ayup/go/cli/push"
	"premai.io/Ayup/go/cli/state"
	"premai.io/Ayup/go/internal/conf"
	"premai.io/Ayup/go/internal/semver"
	"premai.io/Ayup/go/internal/terror"
	ayTrace "premai.io/Ayup/go/internal/trace"
	"premai.io/Ayup/go/internal/tui"
)

type Globals struct {
	Ctx    context.Context
	Tracer trace.Tracer
	Logger *slog.Logger
}

type PushCmd struct {
	Assistant string `env:"AYUP_ASSISTANT_PATH" help:"The path of a local assistant to use during this operation. To push multiple assistants see 'ay assistants'" type:"path"`

	Host       string `env:"AYUP_PUSH_HOST" default:"localhost:50051" help:"The location of a service we can push to"`
	P2pPrivKey string `env:"AYUP_CLIENT_P2P_PRIV_KEY" help:"Secret encryption key produced by 'ay key new'"`
}

func ensurePath(ctx context.Context, inPath string) (string, error) {
	if inPath != "" {
		return inPath, nil
	}

	outPath, err := os.Getwd()
	if err != nil {
		return "", terror.Errorf(ctx, "getwd: %w", err)
	}

	return outPath, nil
}

func (s *PushCmd) Run(g Globals) (err error) {
	pprof.Do(g.Ctx, pprof.Labels("command", "push"), func(ctx context.Context) {
		var path string
		path, err = ensurePath(ctx, cli.App.Path)
		if err != nil {
			return
		}

		p := push.Pusher{
			Host:         s.Host,
			P2pPrivKey:   s.P2pPrivKey,
			AssistantDir: s.Assistant,
			SrcDir:       path,
		}

		err = p.Run(pprof.WithLabels(g.Ctx, pprof.Labels("command", "push")))
	})

	return
}

type LoginCmd struct {
	Host       string `arg:"" env:"AYUP_LOGIN_HOST" help:"The server's P2P multi-address including the peer ID e.g. /dns4/example.com/50051/p2p/1..."`
	P2pPrivKey string `env:"AYUP_CLIENT_P2P_PRIV_KEY" help:"The client's private key, generated automatically if not set, also see 'ay key new'"`
}

func (s *LoginCmd) Run(g Globals) error {
	l := login.Login{
		Host:       s.Host,
		P2pPrivKey: s.P2pPrivKey,
	}

	return l.Run(g.Ctx)
}

type KeyNewCmd struct{}

func (s *KeyNewCmd) Run(g Globals) error {
	return key.New(g.Ctx)
}

type StateAssistantCmd struct {
	Name string `arg:"" optional:"" help:"The name of the assistant to set. Leave blank to see the current one."`
}

func (s *StateAssistantCmd) Run(g Globals) error {
	if s.Name != "" {
		return state.SetAssistant(g.Ctx, cli.App.Path, s.Name)
	}

	if err := state.HasAyup(g.Ctx, cli.App.Path); err != nil {
		return err
	}

	return state.ShowAssistant(g.Ctx, cli.App.Path)
}

type AssistantsPush struct {
	Path string `arg:"" optional:"" help:"The path to the assistant's source directory"`
}

func (s *AssistantsPush) Run(g Globals) error {
	path, err := ensurePath(g.Ctx, s.Path)
	if err != nil {
		return err
	}

	return assistants.Push(g.Ctx, cli.Assistants.Host, cli.Assistants.P2pPrivKey, path)
}

type AssistantsList struct{}

func (s *AssistantsList) Run(g Globals) error {
	return assistants.List(g.Ctx, cli.Assistants.Host, cli.Assistants.P2pPrivKey)
}

var cli struct {
	Login LoginCmd `cmd:"" help:"Login to the Ayup service"`

	Daemon struct {
		Start           DaemonStartCmd           `cmd:"" help:"Start an Ayup service Daemon"`
		StartInRootless DaemonStartInRootlessCmd `cmd:"" passthrough:"" help:"Start a utility daemon to do tasks such as port forwarding in the Rootlesskit namesapce" hidden:""`
	} `cmd:"" help:"Self host Ayup on Linux"`

	Key struct {
		New KeyNewCmd `cmd:"" help:"Create a new private key"`
	} `cmd:"" help:"Manage encryption keys used by Ayup"`

	App struct {
		Path string `env:"AYUP_APP_PATH" help:"The path to application source directory. The current working directory is used if not set"`

		Push      PushCmd           `cmd:"" help:"Figure out how to deploy your application"`
		Assistant StateAssistantCmd `cmd:"" help:"Set or get the first assistant to run. Left unset we'll try to detect what to run"`
	} `cmd:"" help:"Manage the application state"`

	Assistants struct {
		Host       string `env:"AYUP_PUSH_HOST" default:"localhost:50051" help:"The location of a service we can push to"`
		P2pPrivKey string `env:"AYUP_CLIENT_P2P_PRIV_KEY" help:"The client's private key, generated automatically if not set, also see 'ay key new'"`

		Push AssistantsPush `cmd:"" help:"Upload a 'local' assistant to the server using its source"`
		List AssistantsList `cmd:"" help:"List the available assistants on the server"`
	} `cmd:"" help:"Manage build and deployment assistants"`

	// maybe effected by https://github.com/open-telemetry/opentelemetry-go/issues/5562
	// also https://github.com/moby/moby/issues/46129#issuecomment-2016552967
	TelemetryEndpoint       string `group:"monitoring" env:"OTEL_EXPORTER_OTLP_ENDPOINT" help:"the host that telemetry data is sent to; e.g. http://localhost:4317"`
	TelemetryEndpointTraces string `group:"monitoring" env:"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT" help:"the host that traces data is sent to http://localhost:4317"`
	ProfilingEndpoint       string `group:"monitoring" env:"PYROSCOPE_ADHOC_SERVER_ADDRESS" help:"URL performance data is sent to; e.g. http://localhost:4040"`
}

func Main(version []byte) {
	ctx := context.Background()

	// Disable dynamic dark background detection
	// https://github.com/charmbracelet/lipgloss/issues/73
	lipgloss.SetHasDarkBackground(termenv.HasDarkBackground())
	titleStyle := tui.TitleStyle
	versionStyle := tui.VersionStyle
	errorStyle := tui.ErrorStyle

	if err := semver.SetAyupVersion(version); err != nil {
		log.Fatalln(err)
	}
	version = semver.GetAyupVersion().Bytes()
	fmt.Print(titleStyle.Render("Ayup!"), " ", versionStyle.Render("v"+string(version), "\n\n"))

	confDir := conf.UserConfigDir()
	godotenvLoadErr := godotenv.Load(filepath.Join(confDir, "env"))

	ktx := kong.Parse(&cli, kong.UsageOnError(), kong.Description("Just make it run!"))

	ayTrace.SetupPyroscopeProfiling(cli.ProfilingEndpoint)

	if cli.TelemetryEndpoint != "" || cli.TelemetryEndpointTraces != "" {
		stopTracing, err := ayTrace.SetupOTelSDK(ctx)
		if err != nil {
			log.Fatalln(err)
		}
		defer func() {
			if err := stopTracing(ctx); err != nil {
				log.Fatalln(err)
			}
		}()
	}

	tracer := otel.Tracer("premai.io/Ayup/go/internal/trace")
	logger := otelslog.NewLogger("premai.io/Ayup/go/internal/trace")

	ctx = ayTrace.SetSpanKind(ctx, trace.SpanKindClient)
	ctx, span := tracer.Start(ctx, "main")
	defer span.End()

	terror.Ackf(ctx, "godotenv load: %w", godotenvLoadErr)

	err := ktx.Run(Globals{
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
