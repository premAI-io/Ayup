# From https://mitchellh.com/writing/nix-with-dockerfiles
FROM nixos/nix:latest AS builder

COPY . /tmp/build
WORKDIR /tmp/build

RUN mkdir /tmp/nix-store-closure
RUN --mount=type=cache,target=/nix/store,from=nixos/nix:latest,source=/nix/store nix \
    --extra-experimental-features "nix-command flakes" \
    --option filter-syscalls false \
    build && \
    cp -R $(nix-store -qR result/) /tmp/nix-store-closure && \
    cp -R result/bin ./

FROM scratch

WORKDIR /bin

COPY --from=builder /tmp/nix-store-closure /nix/store
COPY --from=builder /tmp/build/bin/ay ./ay
CMD ["/bin/ay"]
