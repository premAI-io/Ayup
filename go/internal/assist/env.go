package assist

import (
	"context"
	"os"

	"go.opentelemetry.io/otel/attribute"
	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"

	"github.com/joho/godotenv"
	"github.com/moby/buildkit/client/llb"
)

// Load the .ayup-env file and then delete it
func LoadEnv(ctx context.Context, path string) (map[string][]byte, []llb.RunOption, error) {
	providerMap := make(map[string][]byte)
	var secretsRunOpts []llb.RunOption

	env, err := godotenv.Read(path)
	if err != nil && os.IsNotExist(err) {
		trace.Event(ctx, "env not found", attribute.String("path", path))
		return providerMap, secretsRunOpts, nil
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
