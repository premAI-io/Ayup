# From https://mitchellh.com/writing/nix-with-dockerfiles
FROM nixos/nix:latest AS builder

WORKDIR /tmp
RUN mkdir -p ./nix-store-closure

COPY . /tmp/build
WORKDIR /tmp/build

RUN --mount=type=cache,target=/nix/store,from=nixos/nix:latest,source=/nix/store nix \
    --extra-experimental-features "nix-command flakes" \
    --option filter-syscalls false \
    build .#server && \
    cp -R $(nix-store -qR result/) /tmp/nix-store-closure && \
    cp -R result/bin ./

WORKDIR /tmp/root
RUN mkdir -p ./bin
RUN cp -R $(nix-store -qR /nix/store/*-shadow-*/) /tmp/nix-store-closure
RUN cp $(ls -1 /nix/store/*-shadow-*/bin/newuidmap) ./bin/newuidmap && \
    chmod u+s ./bin/newuidmap && \
    cp $(ls -1 /nix/store/*-shadow-*/bin/newgidmap) ./bin/newgidmap && \
    chmod u+s ./bin/newgidmap

FROM nixos/nix:latest AS setup

WORKDIR /tmp/root
RUN mkdir -p ./etc/ayup ./var/lib/ayup ./run/ayup ./tmp ./bin \
    ./run/buildkit ./run/runc ./var/lib/cni
RUN chown 1000:1000 ./etc/ayup ./var/lib/ayup ./run/ayup ./tmp \
    ./run/buildkit ./run/runc ./var/lib/cni
RUN echo 'ayup:x:1000:100:Ayup user:/:/bin/ay' > ./etc/passwd
RUN echo 'ayup:100000:65536' > ./etc/subgid
RUN echo 'ayup:100000:65536' > ./etc/subuid

FROM scratch

COPY --from=builder /tmp/root /
COPY --from=setup /tmp/root /
COPY --from=builder /tmp/nix-store-closure /nix/store
COPY --from=builder /tmp/build/bin/ay /bin/ay

VOLUME ["/etc/ayup", "/var/lib/ayup", "/run/ayup"]

USER 1000:100

ENV PATH=/bin
ENV XDG_RUNTIME_DIR=/run
ENV XDG_DATA_HOME=/var/lib
ENV XDG_CONFIG_HOME=/etc

CMD ["/bin/ay"]
