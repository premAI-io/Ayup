# Development

Ayup is a standard Go project and thus easy to build in most enviroments. However Nix
is used to provide the reference development and build environment.

1. Install Nix with flakes/"experimental" features enabled (e.g. use https://github.com/DeterminateSystems/nix-installer)
2. `cd Ayup`

Then there is a choice between using the Nix dev shell...

3. `nix develop` (I add `--command fish` to my nix command to use Fish instead of Bash)
4. `protoc --go_out=go/internal --go-grpc_out=go/internal grpc/srv/lib.proto --go_opt=paths=source_relative --go-grpc_opt=paths=source_relative`
5. `go build -race -o bin/ay ./go`
6. `./bin` is added to the path by Nix so now you can run `ay`

Or using Nix to build/run the project...

3. `nix run .#dev` in another terminal to start buildkit
4. `nix build` build the whole project
5. `nix run .#cli` run the cli

Presently Docker and buildkit are required to be present. Buildkit is provided by Nix,
but Docker needs to be installed by other means.

## Logs and tracing

Open Telemetry is used to collect ~~logs and~~ traces which requires some kind of collector and UI.
To use Jaeger on your local system do

```sh
docker run -d --name jaeger \
                    -e COLLECTOR_OTLP_ENABLED=true \
                    -p 16686:16686 \
                    -p 4317:4317 \
                    -p 4318:4318 \
                    jaegertracing/all-in-one:latest
```

Ayup only sends traces if the standard environment variable is set

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 ay ...
```

You can view the traces at `http://localhost:16686` or wherever the collector/viewer is hosted.

Continuous tracing can be collected with Pyroscope.

```sh
docker run -d -p 4040:4040 grafana/pyroscope
```

And the environment var

```sh
PYROSCOPE_ADHOC_SERVER_ADDRESS=http://localhost:4040 ay ...
```
