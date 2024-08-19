# Vision

Ayup will provide the framework necessary to analyse, modify, build, test, deploy and monitor code
on a remote machine easily. It will allow creating a tight feedback loop that incorporates code
analysis, GenAI and user feedback all on infrastructure of your choosing. 

It will be an all-in-one interactive CI/CD tool that is itself easy to deploy and use. It will allow
workflows that require user interaction and feedback from deployment.

![ayup-push-1](https://github.com/user-attachments/assets/9b7c5ff9-e2a3-4a90-a0d8-4262c78dc6d5)

Both the Ayup server and client are open source. The Ayup server can run on most Linux systems and
the client on Linux, Mac and (eventually) Windows. The intention is that it is easy for you to self
host the Ayup server.

Web applications have their ports forwarded to the client. Allowing them to be accessed as if they
were running locally. Applications can also be served on a sub-domain of the server via the builtin
proxy.

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
- [x] Secure server login and connection

In the pipeline (in no particular order)

- [ ] Detect appropriate ports to forward
- [ ] Multiple simultaneous applications
- [ ] Pluggable analysis/build/run step(s)
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

## Config

All of Ayup's configuration is done via environment variables or command line switches. However you
can also set environment variables in `~/.config/ayup/env` which is in the usual 
[dotenv format](https://github.com/joho/godotenv).

Settings you choose interactively will be persisted to the env file if possible. Command line switches and
environment variables take precedence over the env file.

You can see all available config using the `--help` switch e.g. `ay push --help`, `ay daemon start
--help`

## Server

Presently the server has three prerequisites: Containerd, Buildkitd and Nerdctl. We plan to bundle
or eliminate them, but for now you need to install them or use Nix as described in the
development section.

### Containerd

This component is common and used by both Docker and Kubernetes. It needs to be running on your
system and you need to specify it's address with `AYUP_CONTAINERD_ADDR`. Places you can find it are: 
`/run/containerd/containerd.sock`, `/run/docker/containerd/containerd.sock` or
`/run/k3s/containerd/containerd.sock`.

### Buildkitd

This is also used by Docker, but the buildkit daemon may not be running or may be configured with
the wrong settings. The easiest way to run it with the right configuration is using Nix as described
in the development section.

It could also be started with  `buildkitd --oci-worker false --containerd-worker-addr $AYUP_CONTAINERD_ADDR`

It's socket address can be configured with `AYUP_BUILDKITD_ADDR` if it's different from
`unix:///run/buildkit/buildkitd.sock`.

### Nerdctl

This simply needs to be installed on your system. If your package manager does not have it, then Nix
can be used to get it as described in the development section.

### Start

To start Ayup listening for local connections do

```sh
$ ay daemon start
```

To run it securely on a remote computer listening on all addresses do

```sh
$ ay daemon start --host=/ip4/0.0.0.0/tcp/50051
```

This sets the listen address to a libp2p multiaddress, when Ayup sees this it will only allow
encrypted connections using libp2p.

Ayup will print details on how to login to the server from a client. This requires shell access to
the Ayup server so that you can interactively authorize the client.

Clients can also be pre-authorized by adding their peer IDs to `AYUP_P2P_AUTHORIZED_CLIENTS`

## Client

If the Ayup server is running locally, then all you need to do is change to a source code directory
and run `ay push`

Otherwise you first need to login. The server prints the login command you need to use, it will look
something like:

```
$ ay login /ip4/192.168.0.1/tcp/50051/p2p/1...
```

If you need to get the clients peer ID to pre-authorize it then just run login with a nonsense
address

```
$ ay login foo
```

Login always prints the client's peer ID. 

The login command will set `AYUP_PUSH_HOST` in `~/.config/ayup/env` to the address we used to login
to. So that `ay push` will use it by default. You can override it in the environment or by using
`--host`.

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

Ayup is a standard Go project and thus easy to build in most environments. However Nix
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

# Follow

If you are interested in following Ayup's development then see [the discussions dev log](https://github.com/premAI-io/Ayup/discussions/categories/announcements)
or find me (Richard Palethorpe) on social media.

# Contact

The main point of contact for this project is Richard Palethorpe, richard@premai.io. You can use the discussions, 
e-mail or find me elsewhere with questions, suggestions or feedback.
