//go:build linux

package ay

import (
	"context"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	"premai.io/Ayup/go/inrootless"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/srv"
)

type DaemonStartCmd struct {
	Host           string `env:"AYUP_DAEMON_HOST" default:":50051" help:"The addresses and port to listen on"`
	ContainerdAddr string `env:"AYUP_CONTAINERD_ADDR" help:"The path to the containerd socket if not using Docker's" default:"/var/run/docker/containerd/containerd.sock"`

	P2pPrivKey           string `env:"AYUP_SERVER_P2P_PRIV_KEY" help:"The server's private key, generated automatically if not set, also see 'ay key new'"`
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
			TmpDir:         tmp,
			SrcDir:         filepath.Join(tmp, "src"),
			ImgTarPath:     filepath.Join(tmp, "image.tar"),
			ImgName:        "docker.io/richardprem/ayup:test",
			Host:           s.Host,
			ContainerdAddr: s.ContainerdAddr,
			P2pPrivKey:     s.P2pPrivKey,
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

type DaemonStartInRootlessCmd struct {
	BuildkitArgs []string `arg:"" help:"Buildkitd's arguments"`
}

func (s *DaemonStartInRootlessCmd) Run(g Globals) (err error) {
	pprof.Do(g.Ctx, pprof.Labels("command", "daemon startinrootless"), func(ctx context.Context) {
		err = inrootless.RunServer(ctx, s.BuildkitArgs)
	})

	return
}
