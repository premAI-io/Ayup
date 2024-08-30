{
    lib, stdenv, makeWrapper, version, buildkit, cli
}:
let
    runtimeDeps = [ buildkit ];
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
            wrapProgram $out/bin/ay \
              --prefix PATH : ${ lib.makeBinPath runtimeDeps }
       '';
    }
