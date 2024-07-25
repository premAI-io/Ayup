{
    buildGoModule, version, callPackage,
    # lib,
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
  # Use lib.fakeHash when updating deps
  vendorHash = "sha256-3hTfXDmIGh9HFUa/5LKheknw3ZP/80NATINDZvbizEE="; # lib.fakeHash;
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
