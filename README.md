# Vision

Ayup will figure out how to build, run and serve some AI/ML inference code you provide on a remote
(or local) computer. It saves you time when dealing with unfamiliar code and makes using a remote
machine for building and executing code easy.

Ayup avoids relying on you to read documentation and write configuration files. Instead it tries to
solve problems with you interactively.

![ayup-push-1](https://github.com/user-attachments/assets/9b7c5ff9-e2a3-4a90-a0d8-4262c78dc6d5)

Both the Ayup server and client are open source. The Ayup server can run on most Linux systems and
the client on Linux, Mac and (eventually) Windows. The intention is that it is easy for you to self
host the Ayup server.

Web applications have their ports forwarded to the client. Allowing them to be accessed as if they
were running locally. Applications can also be served on a sub-domain of the server via the builtin
proxy.

The initial focus of Ayup is on AI/ML applications which provide some inference service.

# State / Roadmap

## YouTube: Demo

[![YouTube: Demo](http://i.ytimg.com/vi/umWNG89BXVE/hqdefault.jpg)](https://www.youtube.com/watch?v=umWNG89BXVE)

## Tasks

Ayup is in the early stages of production. Some of the things that have been done so far are

- [x] Quick source upload
- [x] Build and serve Python applications of a particular form
- [x] Port forwarding to client with fixed ports
- [x] Containerd support (allows instant loading of containers and future k8s support)
- [x] Subdomain routing
- [x] Build and run applications with a Dockerfile

In the pipeline (in no particular order)

- [ ] Detect appropriate ports to forward
- [ ] Multiple simultaneous applications
- [ ] Pluggable analysis/build/run step(s)
- [ ] Secure server login and connection
- [ ] Bundle rootless Containerd, Buildkit, Nerdctl with the server
- [ ] Watch mode for build and deploy on save
- [ ] Deploy itself in daemon mode

# Install

The client CLI can be installed by copying the following into a terminal session:

```sh
curl -#L https://raw.githubusercontent.com/premAI-io/Ayup/main/script/install.sh | sh
```

Or you can download the executable from the [release page](https://github.com/premAI-io/Ayup/releases/latest).
It's just one file, you can copy it to `/usr/bin` or wherever you put executables on your system.

Presently the server is in the same executable as the client. However it has some dependencies which
are a pain. Eventually we'll magic these away, but in the mean time see the Development section.

# Running

## Client

Enter the directory where the source of the project you wish to run is and do

```sh
$ ay push
```

For now this will stupidly try to connect to localhost. Until we create a secure login process
you will need to set the remote address (see `ay --help`)

## Server

The server can be run as root with

```sh
$ ay daemon start
```

This requires a Buildkitd instance to be running and pointed at a Containerd worker. This is not
such a common thing, so see the development section for now which has details on using Nix to get
them. Eventually we will bundle these up.

Often you will need to set the Containerd address which can be done with an environment var or
command line option, see `ay daemon start --help`.

Installing Docker or Kubernetes will provide a Containerd instance. It's socket will be somewhere
like `/run/containerd/containerd.sock`, `/run/docker/containerd/containerd.sock` or
`/run/k3s/containerd/containerd.sock`.

## Examples

There is an [examples directory](https://github.com/premAI-io/Ayup/tree/main/examples) that contains
some applications that are known to work with Ayup.

For example you can do

```sh
$ cd $AYUP_SRC/examples/hello-world-flask
$ ay push
```

You can also run it on its self

```sh
$ cd $AYUP_SRC
$ ay push
```

Presently it just produces the help output.

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

3. `sudo nix run .#dev` in another terminal to start buildkit
4. `sudo nix run .#server` run the server
5. `nix run .#cli` run the cli

Also you don't need to Git clone this project onto a system to run it with Nix. You can run the
flake from this repo with

`sudo nix run github:premAI-io/Ayup#<dev,server,cli>`

Or if you want to try out a dev branch

`sudo nix run github:<user>/<repo>/<branch>#<dev,server,cli>`

# Logs and tracing

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
