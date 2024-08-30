{ cni-plugins }:

cni-plugins.overrideAttrs (oldAttrs: {
  postInstall = ''
    for bin in $out/bin/*; do
      new_name=$(basename "$bin" | sed 's/^/buildkit-cni-/')
      mv "$bin" "$out/bin/$new_name"
    done
  '';
})
