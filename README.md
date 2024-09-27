# Vision

🚀 Quickly and securely turn any Linux box into a build and deployment assistant.

![ayup-push-2](https://github.com/user-attachments/assets/b96a0bf8-f515-4187-a2da-e02274b31e22)

🔓 Both the Ayup server and client are open source. The Ayup server can run on most Linux systems and
the client on Linux, Mac and (eventually) Windows.

🌐 Web applications have their ports forwarded to the client. Allowing them to be accessed as if they
were running locally. Applications can also be served on a sub-domain of the server via the builtin
proxy.

🛠️ Figuring out how to generate, build or serve your app is left to [pluggable
assistants](docs/assistants.md). These can be written in any language and are ran inside a
container. They can be chained together like middleware.

# State / Roadmap

Ayup is in the early stages of production. Presently the focus is on generalising it by offloading
work into generic assistants.

## Tasks

Some of the things that have been done so far are

- [x] Quick source upload
- [x] Build and serve Python applications of a particular form
- [x] Port forwarding to client with fixed ports
- [x] Subdomain routing
- [x] Build and run applications with a Dockerfile
- [x] Secure server login and connection
- [x] Rootless (run as a normal user)
- [x] Pluggable analysis/build/run step(s) (Assistants)
- [x] Detect appropriate ports to forward (In Dockefile)

In the pipeline (in no particular order)

- [ ] Multiple simultaneous applications
- [ ] All-in-one executable
- [ ] Watch mode for build and deploy on save
- [ ] Deploy itself in daemon mode

# Install

## Client

The client CLI can be installed by copying the following into a terminal session:

```sh
curl -#L https://raw.githubusercontent.com/premAI-io/Ayup/main/script/install.sh | sh
```

Or you can download the executable from the [release page](https://github.com/premAI-io/Ayup/releases/latest).
It's just one file, you can copy it to `/usr/bin` or wherever you put executables on your system.

Presently the server is in the same executable as the client. However it has some dependencies.

## Server

There are two easy ways to install the server: Nix and Docker. In both cases installing and running
are practically the same operation.

For Docker you can do

```sh
$ mkdir ~/.config/ayup
$ docker run --privileged --rm -it -v ~/.config/ayup:/etc/ayup -p 50051:50051 premai/ayup:latest ay daemon start --host=/ip4/0.0.0.0/tcp/50051
```

> [!TIP]
> The Ayup server only supports Linux, but Docker Desktop should be able to run it on Windows
> and Mac.

With Nix you can start a shell and run `ay`

```sh
$ nix shell github:premAI-io/Ayup
$ ay daemon start --host=/ip4/0.0.0.0/tcp/50051
```

See the development section for more information on Nix.

To install the server manually, follow the instructions for the client and then install the
dependencies listed in `distros/nix/server.nix`.

# Running

## Server

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

> [!NOTE]
> The IP address in the printed multiaddress will only be accessible locally when using Docker. It may need
> to be replaced with the host's public IP or DNS name. For e.g
> `/dns4/example.com/tcp/50051/p2p/1...`

Clients can also be pre-authorized by adding their peer IDs to `AYUP_P2P_AUTHORIZED_CLIENTS`

## Client

If the Ayup server is running locally, then all you need to do is change to a source code directory
and run `ay app push`

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
to. So that `ay app push` will use it by default. You can override it in the environment or by using
`--host`.

## Config

All of Ayup's configuration is done via environment variables or command line switches. However you
can also set environment variables in `~/.config/ayup/env` which is in the usual
[dotenv format](https://github.com/joho/godotenv).

Settings you choose interactively will be persisted to the env file if possible. Command line switches and
environment variables take precedence over the env file.

You can see all available config using the `--help` switch e.g. `ay push --help`, `ay daemon start
--help`

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
4. `script/gen-src.sh`
5. `go build -race -o bin/ay ./go`
6. `./bin` is added to the path by Nix so now you can run `ay`

Or using Nix to build/run the project...

3. `nix run .#server` run the server
4. `nix run .#cli` run the cli

Also you don't need to Git clone this project onto a system to run it with Nix. You can run the
flake from this repo with

`nix run github:premAI-io/Ayup#<server,cli>`

Or if you want to try out a dev branch

`nix run github:<user>/<repo>/<branch>#<server,cli>`

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

Note that the collector must support gRPC traces.

Ayup only sends traces if the standard environment variable is set

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317 ay ...
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
