{
  description = "Ayup";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = import nixpkgs {
        inherit system;
      };
      buildkitDevPkgs = with pkgs; [
          slirp4netns
          rootlesskit
          runc
          buildkit
          nerdctl
      ];
      goDevPkgs = with pkgs; [
        go
        golangci-lint
        protobuf
        protoc-gen-go
        protoc-gen-go-grpc
      ];
      pyAnalysis = with pkgs; [
        pipreqs
      ];
      version = builtins.readFile ./go/version.txt;
      src = pkgs.callPackage ./distros/nix/src.nix {
          inherit version;
          dontPatchShebangs = true;
      };
      cli = pkgs.callPackage ./distros/nix/cli.nix { inherit version; };
      cli-darwin-arm64 = pkgs.callPackage ./distros/nix/cli-cross.nix {
        inherit version;
        GOOS = "darwin";
        GOARCH = "arm64";
      };
      cli-darwin-amd64 = pkgs.callPackage ./distros/nix/cli-cross.nix {
        inherit version;
        GOOS = "darwin";
        GOARCH = "amd64";
      };
      cli-linux-arm64 = pkgs.callPackage ./distros/nix/cli-cross.nix {
        inherit version;
        GOOS = "linux";
        GOARCH = "arm64";
      };
      cli-linux-amd64 = pkgs.callPackage ./distros/nix/cli-cross.nix {
        inherit version;
        GOOS = "linux";
        GOARCH = "amd64";
      };
  in
    {
      devShells.${system}.default = pkgs.mkShell {
        nativeBuildInputs = [ pkgs.pkg-config ];
        buildInputs = buildkitDevPkgs ++ goDevPkgs ++ pyAnalysis;
        shellHook = ''
            export PATH=$PATH:$PWD/bin
        '';
      };

      packages.${system} = {
        inherit src;
        inherit cli cli-darwin-amd64 cli-darwin-arm64 cli-linux-arm64 cli-linux-amd64;
        default = cli;
        dev = pkgs.stdenv.mkDerivation {
          pname = "dev";
          inherit version;
          src = ./script/.;
          nativeBuildInputs = [ pkgs.makeWrapper ];
          buildInputs = buildkitDevPkgs;
          installPhase = ''
            mkdir -p $out/bin
            cp start-dev-services.sh $out/bin/dev
            wrapProgram $out/bin/dev \
              --prefix PATH : ${ pkgs.lib.makeBinPath buildkitDevPkgs }
          '';
        };
     };
    };
}
