{
  buildGoModule,
  version,
  callPackage,
  golangci-lint,
  vendorHash,
}:
let
  srcWithProtobuf = callPackage ./src.nix { inherit version; };
in
buildGoModule {
  pname = "ayup-cli";
  meta.mainProgram = "ay";
  version = version;
  src = srcWithProtobuf;
  modRoot = "./go";
  # Use lib.fakeHash when updating deps
  inherit vendorHash;
  proxyVendor = true; # Avoids workspace mode error
  postInstall = ''
    mv $out/bin/go $out/bin/ay
  '';
  nativeBuildInputs = [ golangci-lint ];
  doCheck = true;
  checkPhase = ''
    HOME=$TMPDIR golangci-lint -v run
  '';
}
