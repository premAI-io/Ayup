{
    stdenv, version, protobuf, protoc-gen-go, protoc-gen-go-grpc,
    dontPatchShebangs ? false
}:
stdenv.mkDerivation {
    pname = "ayup-src";
    inherit version dontPatchShebangs;
    src = ../..;
    nativeBuildInputs = [
        protobuf
        protoc-gen-go
        protoc-gen-go-grpc
  ];
  buildPhase = ''
    mkdir -p $out
    cd $out
    cp -r $src/* ./
    chmod -R u+w go/internal/grpc
    source script/gen-src.sh
    '';
}
