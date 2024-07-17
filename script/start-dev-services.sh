#!/bin/sh -eu

echo Starting rootless buildkit
echo Test with buildctl --addr unix:///run/user/$UID/buildkit/buildkit.sock du

rootlesskit --net=slirp4netns --copy-up=/etc --disable-host-loopback buildkitd
