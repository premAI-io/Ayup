{
    buildGoModule, version, callPackage, vendorHash,
    GOOS ? "linux",
    GOARCH ? "amd64",
}:
let
    srcWithProtobuf = callPackage ./src.nix {
        dontPatchShebangs = true;
        inherit version;
    };
in
(buildGoModule {
  pname = "ayup-cli";
  meta.mainProgram = "ay";
  version = version;
  src = srcWithProtobuf;
  modRoot = "./go";
  inherit vendorHash;
  # Avoids workspace mode error
  proxyVendor = true;
  postInstall = ''
    if [[ -d $out/bin/${GOOS}_${GOARCH} ]]; then
        mv $out/bin/${GOOS}_${GOARCH}/go $out/bin/ay
        rmdir $out/bin/${GOOS}_${GOARCH}
    else
        mv $out/bin/go $out/bin/ay
    fi
  '';

  CGO_ENABLED = 0;
}).overrideAttrs (old: old // { inherit GOOS GOARCH; })
