# Assistant Overview

Generating, building and deploying code is handled by containerized Assistants. When you push to
Ayup, a series of Assistants will run in a chain. Each one performing a transformation on the
application's code and state. Then deciding what to do next.

This may sound like a CI/CD pipeline and is very similar in principal. Where it differs is that
Assistants are intended to be interactive and loops are not forbidden.

There are two major types of Assistant:

1. Builtin
2. External

## Builtin

The Builtin Assistants are, as the name suggests, part of Ayup's core. They are written in Go and
have complete access to Ayup's internals. Of note is the Exec assistant which runs the application
once it has been built by previous assistants.

However you are not required to run this assistant or any other. You can write an assistant which
will build and execute your project in one go. Using the exec assistant is preferable though as in
the future it could be swapped out for different types of exec assistant.

For example we could introduce an exec assistant which runs the final application binary in a micro
VM or a Kubernetes cluster. We also could add instrumentation to the exec assistant, let's say a
port monitor which finds the ports it opens or a fancy eBPF performance tracer.

## External

External assistants can be written in any language which can read and write files. At a minimum an
assistant should have a Dockerfile and a name file.

When the assistant is executed it will have some files from the user and previous assistants mounted
in `/in` and can write files to `/out`. These files include Ayup state and the application. 

The state files are all relatively simple. The most complex is presently the ports file which can
contain a JSON array of ports to forward. The idea is that a language with a very limited standard
library can easily read and write these files.

> [!TIP]
> The .ayup directory contains the Ayup application state after calling `ay app push`

In the future there will also be a socket for the assistant to communicate with the user.

External assistants can further be separated into two categories:

1. local
2. remote

Local assistants are ones added to an Ayup instance with `ay assistants push` or 
`ay app push --assistant=<path>`.

Remote assistants are distributed with Ayup and can be found in the `/assistants` directory of
Ayup's source tree. In the future they could also be distributed via a container registry.

# Using Assistants

If you run `ay push` on a project without specifying an assistant, then it will try to find one for
you. The aspiration of Ayup is that you should not have to specify anything unless you want to.

Aspirations aside, you can specify a particular assistant with `ay app assistant <name>`. To get
list of assistant names you can use `ay assistants list`. Names all have the form `<type>:<name>`, for
e.g. `local:prem` or `builtin:dockerfile`.

For now you have to specify the full name, but in the future the bit before the ':' could be
omitted.

More assistants can be added by running `ay assistants push` on their source path, for e.g. `ay
assistants push /examples/assistants/prem`.

> [!TIP]
> For now Assistants that require secrets or environment variables can use .ayup-env placed in their
> source directory. It will get uploaded during push. In the future a secrets will be
> added to the CLI.

# Creating Assistants

## name

First create a name file, it's just called name and contains the assistant's name without its type
(without `<type>:`).

## Dockerfile

Then create an ordinary Dockerfile that contains an `ENTRYPOINT` or `CMD`. For example:

```Dockerfile
FROM busybox

COPY --link --chmod=555 . /assistant

ENTRYPOINT ["/assistant/cmd"]
```

It's recommended to put the assistant's scripts or binaries in `/assistant`.

The directories `/in` and `/out` are used as mount points, so anything written to them
won't be available at runtime.

## IO

The assistant should read data from `/in` and write it to `/out`.

### /in/state and /out/state

This contains the application state from the user (uploaded from `.ayup` in the pushed directory).

The files it (probably) contains are:

- `cmd`: A JSON array of strings containing the command line to run. It is used by `builtin:exec` and resembles a Dockerfile's `CMD`
- `log`: Logs from a previous execution of the application, usually output by `builtin:exec`
- `next`: The next assistant to run e.g `builtin:exec`
- `ports`: A JSON array of numbers specifying ports to forward or expose
- `version:`: The version of Ayup this state directory was created by
- `workingdir:`: The path `cmd` will be run in, similar to WORKINGDIR in a dockerfile

Often you can ignore most of these, instead writing out to a Dockerfile and setting `next` to
`builtin:dockerfile`.

You can stop execution by not writing to `/out/next` or by exiting with a non-zero status.

Unlike with `/out/app` you don't have to copy files from `/in` to `/out` to keep the values in the
them. If you want to clear a state you have to write a null or empty value to its out file.

### /in/app and /out/app

These contain the application code, but with the `.ayup` directory removed because that is in
`../state`.

Unlike with state files, application files need to be copied from `/in` to `/out` if you wish to
keep them.

# Builtin Assistants

The Builtin Assistants satisfy the `Assistant` interface. They have access to the Buildkit's binary
representation of Dockerfile's called LLB. They also have access to the gRPC stream connected to the
user as well as internal state.

Each builtin can add to or modify the LLB before passing it on. This is quite different from the
external assistants which just modify the apps source or the state files. Generally builtins should
be run after external assistants which are unaware of the LLB.

There's no technical reason why external assistants couldn't interact with the LLB as well, but it
would raise the bar dramatically for Assistant authors.

It's likely that functionality initially implemented in builtins will be moved out into external
assistants.
