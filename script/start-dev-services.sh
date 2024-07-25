#!/bin/sh -eu

echo Starting buildkit

: ${AYUP_CONTAINERD_ADDR:=/run/docker/containerd/containerd.sock}

buildkitd --oci-worker false --containerd-worker-addr $AYUP_CONTAINERD_ADDR
