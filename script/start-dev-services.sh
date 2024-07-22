#!/bin/sh -eu

echo Starting buildkit

buildkitd --oci-worker false --containerd-worker-addr /run/docker/containerd/containerd.sock
