package srv

import (
	"context"
	"os"
	"path/filepath"

	"go.opentelemetry.io/otel/attribute"
	pb "premai.io/Ayup/go/internal/grpc/srv"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"

	"github.com/joho/godotenv"
	"github.com/moby/buildkit/client/llb"
)

// Load the .ayup-env file and then delete it
func (s *Srv) loadAyupEnv(ctx context.Context, src pb.Source) (map[string][]byte, []llb.RunOption, error) {
	var path string
	switch src {
	case pb.Source_app:
		path = s.SrcDir
	case pb.Source_assistant:
		path = s.AssistantDir
	}

	path = filepath.Join(path, ".ayup-env")

	providerMap := make(map[string][]byte)
	var secretsRunOpts []llb.RunOption

	env, err := godotenv.Read(path)
	if err != nil && os.IsNotExist(err) {
		trace.Event(ctx, ".ayup-env not found", attribute.String("path", path))
		return providerMap, secretsRunOpts, err
	}
	defer func() {
		terror.Ackf(ctx, "os Remove: %w", os.Remove(path))
	}()

	if err != nil {
		return nil, nil, terror.Errorf(ctx, "godotenv Read: %w", err)
	}

	for k, v := range env {
		providerMap[k] = []byte(v)
		secretsRunOpts = append(secretsRunOpts, llb.AddSecret(k, llb.SecretAsEnv(true)))
	}

	trace.Event(ctx, ".ayup-env loaded", attribute.String("path", path))

	return providerMap, secretsRunOpts, nil
}
