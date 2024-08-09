package key

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
	"premai.io/Ayup/go/internal/tui"
)

func New(ctx context.Context) error {
	ctx, span := trace.Span(ctx, "key new")
	defer span.End()

	priv, pub, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		return terror.Errorf(ctx, "crypto GenerateEd25519Key: %w", err)
	}

	titleStyle := tui.TitleStyle

	pbPrivKey, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return terror.Errorf(ctx, "crypto marshalPrivateKey: %w", err)
	}

	b64Key := base64.StdEncoding.EncodeToString(pbPrivKey)

	fmt.Println(titleStyle.Render("Private Key: "), b64Key)

	peerId, err := peer.IDFromPublicKey(pub)
	if err != nil {
		return terror.Errorf(ctx, "peer IDFromPublicKey: %w", err)
	}

	fmt.Println(titleStyle.Render("Peer ID: "), peerId)

	return nil

}
