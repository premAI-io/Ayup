package daemon

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"premai.io/Ayup/go/internal/rpc"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
	"premai.io/Ayup/go/internal/tui"
)

func PreauthConf(ctx context.Context, cliPrivKeyB64 string) (peer.ID, string, error) {
	ctx, span := trace.Span(ctx, "preauth")
	defer span.End()

	cliPrivKey, err := rpc.EnsurePrivKey(ctx, "AYUP_CLIENT_P2P_PRIV_KEY", cliPrivKeyB64)
	if err != nil {
		return peer.ID(""), "", err
	}

	cliPeerId, err := peer.IDFromPrivateKey(cliPrivKey)
	if err != nil {
		return peer.ID(""), "", terror.Errorf(ctx, "peer IDFromPrivateKey: %w", err)
	}

	srvPrivKey, pub, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		return peer.ID(""), "", terror.Errorf(ctx, "crypto GenerateEd25519Key: %w", err)
	}

	pbSrvPrivKey, err := crypto.MarshalPrivateKey(srvPrivKey)
	if err != nil {
		return peer.ID(""), "", terror.Errorf(ctx, "crypto marshalPrivateKey: %w", err)
	}

	b64SrvPrivKey := base64.StdEncoding.EncodeToString(pbSrvPrivKey)

	srvPeerId, err := peer.IDFromPublicKey(pub)
	if err != nil {
		return peer.ID(""), "", terror.Errorf(ctx, "peer IDFromPublicKey: %w", err)
	}

	confMap := map[string]string{
		"AYUP_SERVER_P2P_PRIV_KEY":    b64SrvPrivKey,
		"AYUP_P2P_AUTHORIZED_CLIENTS": cliPeerId.String(),
	}

	confText, err := godotenv.Marshal(confMap)
	if err != nil {
		return peer.ID(""), "", terror.Errorf(ctx, "godotenv Marshal: %w", err)
	}

	return srvPeerId, confText, nil
}

func RunPreauth(ctx context.Context, b64privKey string) error {
	srvPeerId, confText, err := PreauthConf(ctx, b64privKey)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, tui.VersionStyle.Render("Printing server environment variables to stdout. You can save this configuration to ~/.config/ayup/env or set the environment some other way"))
	fmt.Fprintln(os.Stderr, tui.VersionStyle.Render(fmt.Sprintf("Connect with something like `ay login /ip4/<ip address>/tcp/50051/p2p/%s`", srvPeerId.String())))
	fmt.Fprintln(os.Stderr)

	fmt.Println(confText)

	return nil
}
