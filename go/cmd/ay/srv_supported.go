//go:build linux

package ay

import (
	"context"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"

	"github.com/joho/godotenv"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.opentelemetry.io/otel/attribute"
	"premai.io/Ayup/go/inrootless"
	"premai.io/Ayup/go/internal/conf"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
	"premai.io/Ayup/go/srv"
)

type DaemonStartCmd struct {
	Host string `env:"AYUP_DAEMON_HOST" default:":50051" help:"The addresses and port to listen on"`

	P2pPrivKey           string `env:"AYUP_SERVER_P2P_PRIV_KEY" help:"The server's private key, generated automatically if not set, also see 'ay key new'"`
	P2pAuthorizedClients string `env:"AYUP_P2P_AUTHORIZED_CLIENTS" help:"Comma deliminated public keys of logged in clients"`

	Aws bool `env:"AYUP_AWS" help:"Indicate we are running in an Amazon ec2 instance and can use services like the secrets store"`

	AssistantsDir string `env:"AYUP_ASSISTANTS_DIR" help:"Local path to the source code for the 'remote' assistants. That is assistants distributed with Ayup or from somewhere other than the client machine"`
}

func (s *DaemonStartCmd) Run(g Globals) (err error) {
	ctx, span := trace.Span(g.Ctx, "run start cmd")
	defer span.End()

	pprof.Do(ctx, pprof.Labels("command", "deamon start"), func(ctx context.Context) {
		var tmp string
		tmp, err = os.MkdirTemp("", "ayup-*")
		if err != nil {
			err = terror.Errorf(ctx, "MkdirTemp: %w", err)
			return
		}

		err = os.MkdirAll(conf.UserRuntimeDir(), 0770)
		if err != nil {
			err = terror.Errorf(ctx, "MkdirAll: %w", err)
			return
		}

		assistantsDataDir := filepath.Join(conf.UserRoot(), "assistants")
		if err = os.MkdirAll(assistantsDataDir, 0700); err != nil {
			err = terror.Errorf(ctx, "os MkdirAll: %w", err)
			return
		}

		r := srv.Srv{
			AssistantDir:        filepath.Join(tmp, "assist"),
			RemoteAssistantsDir: s.AssistantsDir,
			LocalAssistantsDir:  assistantsDataDir,
			AppDir:              filepath.Join(tmp, "app"),
			StateDir:            filepath.Join(tmp, "state"),
			ScratchDir:          filepath.Join(tmp, "scratch"),
			Host:                s.Host,
			P2pPrivKey:          s.P2pPrivKey,
		}

		authedClientsStr := s.P2pAuthorizedClients
		if s.Aws {
			var confStr string
			confStr, err = conf.LoadConfigFromAWS(ctx)
			if err != nil {
				return
			}

			var conf map[string]string
			conf, err = godotenv.Unmarshal(confStr)
			if err != nil {
				err = terror.Errorf(ctx, "godotenv Unmarshal: %w", err)
				return
			}

			if key, ok := conf["AYUP_SERVER_P2P_PRIV_KEY"]; ok {
				trace.Event(ctx, "set private key from AWS config")
				r.P2pPrivKey = key
			}

			if clients, ok := conf["AYUP_P2P_AUTHORIZED_CLIENTS"]; ok {
				trace.Event(ctx, "set authorized clients from AWS config", attribute.String("clients", clients))
				authedClientsStr = clients
			}
		}

		var authedClients []peer.ID
		if authedClientsStr != "" {
			for _, peerStr := range strings.Split(authedClientsStr, ",") {

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

type DaemonStartInRootlessCmd struct {
	BuildkitArgs []string `arg:"" help:"Buildkitd's arguments"`
}

func (s *DaemonStartInRootlessCmd) Run(g Globals) (err error) {
	pprof.Do(g.Ctx, pprof.Labels("command", "daemon startinrootless"), func(ctx context.Context) {
		err = inrootless.RunServer(ctx, s.BuildkitArgs)
	})

	return
}
