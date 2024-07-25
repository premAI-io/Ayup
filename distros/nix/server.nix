{
    lib, stdenv, makeWrapper, version, buildkit, nerdctl, pipreqs, cli
}:
let
    runtimeDeps = [ buildkit nerdctl pipreqs ];
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
