package rpc

import (
	"context"
	"encoding/base64"
	"net"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	attr "go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"premai.io/Ayup/go/internal/conf"
	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/terror"
	ptrace "premai.io/Ayup/go/internal/trace"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	noise "github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	multiaddr "github.com/multiformats/go-multiaddr"

	gostream "github.com/libp2p/go-libp2p-gostream"
)

const Libp2pProtocol = protocol.ID("/ayup/grpc/1.0.0")

func EnsurePrivKey(ctx context.Context, b64PrivKey string) (privKey crypto.PrivKey, err error) {
	if b64PrivKey == "" {
		ptrace.Event(ctx, "creating private key")

		privKey, _, err = crypto.GenerateEd25519Key(nil)
		if err != nil {
			return nil, terror.Errorf(ctx, "crypto GenerateEd25519Key: %w", err)
		}

		pbPrivKey, err := crypto.MarshalPrivateKey(privKey)
		if err != nil {
			return nil, terror.Errorf(ctx, "crypto marshalPrivateKey: %w", err)
		}

		b64Key := base64.StdEncoding.EncodeToString(pbPrivKey)
		_ = conf.Set(ctx, "AYUP_P2P_PRIV_KEY", b64Key)
	} else {
		privKeyPb, err := base64.StdEncoding.DecodeString(b64PrivKey)
		if err != nil {
			return nil, terror.Errorf(ctx, "base64 SetEncoding DecodeString: %w", err)
		}
		privKey, err = crypto.UnmarshalPrivateKey(privKeyPb)
		if err != nil {
			return nil, terror.Errorf(ctx, "crypto UnmarshalPrivateKey: %w", err)
		}
	}

	return
}

func Listen(ctx context.Context, addr string, priv crypto.PrivKey) (net.Listener, host.Host, error) {
	maddr, err := multiaddr.NewMultiaddr(addr)
	if err != nil || priv == nil {
		terror.Ackf(ctx, "new multiaddr: %w", err)

		lis, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, nil, terror.Errorf(ctx, "listen: %w", err)
		}
		return lis, nil, nil
	}

	host, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Security(noise.ID, noise.New),
		libp2p.ListenAddrs(maddr),
	)
	if err != nil {
		return nil, nil, terror.Errorf(ctx, "libp2p new: %w", err)
	}

	for _, maddr := range host.Addrs() {
		ptrace.Event(ctx, "listen", attr.String("maddr", maddr.String()), attr.String("peerID", host.ID().String()))
	}

	lis, err := gostream.Listen(host, Libp2pProtocol)
	if err != nil {
		return nil, host, terror.Errorf(ctx, "gostream Listen: %w", err)
	}

	return lis, host, nil
}

func Client(ctx context.Context, target string, priv crypto.PrivKey) (pb.SrvClient, error) {
	provider := trace.SpanFromContext(ctx).TracerProvider()

	maddr, err := multiaddr.NewMultiaddr(target)
	if err != nil {
		terror.Ackf(ctx, "new multiaddr: %w", err)

		conn, err := grpc.NewClient(target,
			grpc.WithStatsHandler(
				otelgrpc.NewClientHandler(
					otelgrpc.WithTracerProvider(provider),
					otelgrpc.WithMessageEvents(otelgrpc.ReceivedEvents, otelgrpc.SentEvents),
				),
			),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			return nil, err
		}

		return pb.NewSrvClient(conn), nil
	}

	peerInfo, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return nil, terror.Errorf(ctx, "peer addrinfofromp2padd: %w", err)
	}

	host, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Security(noise.ID, noise.New),
		libp2p.NoListenAddrs,
	)
	if err != nil {
		return nil, terror.Errorf(ctx, "libp2p new: %w", err)
	}

	p2pDialer := func(ctx context.Context, _ string) (net.Conn, error) {
		if err := host.Connect(ctx, *peerInfo); err != nil {
			return nil, terror.Errorf(ctx, "host connect: %w", err)
		}

		stream, err := gostream.Dial(ctx, host, peerInfo.ID, Libp2pProtocol)
		if err != nil {
			return nil, terror.Errorf(ctx, "gostream Dial: %w", err)
		}

		return stream, nil
	}

	conn, err := grpc.NewClient("passthrough://"+target,
		grpc.WithStatsHandler(
			otelgrpc.NewClientHandler(
				otelgrpc.WithTracerProvider(provider),
				otelgrpc.WithMessageEvents(otelgrpc.ReceivedEvents, otelgrpc.SentEvents),
			),
		),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(p2pDialer),
	)

	if err != nil {
		return nil, terror.Errorf(ctx, "grpc dial: %w", err)
	}

	return pb.NewSrvClient(conn), nil
}
