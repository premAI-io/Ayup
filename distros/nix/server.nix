{
    lib, stdenv, makeWrapper, callPackage, version, cli,
    buildkit, slirp4netns, rootlesskit, runc, cni, buildkit-cni-plugins,
    util-linux, iproute2, iptables, cacert
}:
let
    src = callPackage ./src.nix { inherit version; };
    runtimeDeps = [
        slirp4netns
        rootlesskit
        runc
        cni
        buildkit-cni-plugins
        buildkit
        util-linux # unshare,nsenter for rootlesskit
        iproute2 # ip for slirp4netns
        iptables # for CNI
    ];
in
    stdenv.mkDerivation {
        pname = "ayup-server";
        meta.mainProgram = "ay";
        inherit version;
        src = cli;
        nativeBuildInputs = [ makeWrapper ];
        buildInputs = runtimeDeps;
        installPhase = ''
            mkdir -p $out/bin
            cp $src/bin/ay $out/bin/

            mkdir -p $out/usr/lib/ayup
            cp -r ${src}/assistants $out/usr/lib/ayup

            wrapProgram $out/bin/ay \
              --prefix PATH : ${ lib.makeBinPath runtimeDeps } \
              --set SSL_CERT_FILE "${cacert}/etc/ssl/certs/ca-bundle.crt" \
              --set-default AYUP_ASSISTANTS_DIR $out/usr/lib/ayup/assistants
       '';
    }
