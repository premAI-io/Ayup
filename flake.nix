{
  description = "Quickly and securely turn any Linux box into a build and deployment assistant";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    nixos-generators = {
        url = "github:nix-community/nixos-generators";
        inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, nixos-generators }:
    let
      system = "x86_64-linux";
      pkgs = import nixpkgs {
        inherit system;
      };
      buildkit-cni-plugins = pkgs.callPackage ./distros/nix/buildkit-cni.nix {};
      buildkitDevPkgs = with pkgs; [
          slirp4netns
          rootlesskit
          runc
          cni
          buildkit-cni-plugins
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
      vendorHash = "sha256-gC1xFyTP6DsMPDtjmhon8HzotBIhGphGcPBPXgFhNN4="; #pkgs.lib.fakeHash;
      cli = pkgs.callPackage ./distros/nix/cli.nix {
        inherit version vendorHash;
      };
      cli-darwin-arm64 = pkgs.callPackage ./distros/nix/cli-cross.nix {
        inherit version vendorHash;
        GOOS = "darwin";
        GOARCH = "arm64";
      };
      cli-darwin-amd64 = pkgs.callPackage ./distros/nix/cli-cross.nix {
        inherit version vendorHash;
        GOOS = "darwin";
        GOARCH = "amd64";
      };
      cli-linux-arm64 = pkgs.callPackage ./distros/nix/cli-cross.nix {
        inherit version vendorHash;
        GOOS = "linux";
        GOARCH = "arm64";
      };
      cli-linux-amd64 = pkgs.callPackage ./distros/nix/cli-cross.nix {
        inherit version vendorHash;
        GOOS = "linux";
        GOARCH = "amd64";
      };
      server = pkgs.callPackage ./distros/nix/server.nix {
        inherit version cli buildkit-cni-plugins;
      };
  in
    {
      devShells.${system}.default = pkgs.mkShell {
        nativeBuildInputs = [ pkgs.pkg-config ];
        buildInputs = buildkitDevPkgs ++ goDevPkgs ++ pyAnalysis;
        shellHook = ''
            export PATH=$PATH:$PWD/bin
            export AYUP_ASSISTANTS_DIR=$PWD/assistants
        '';
      };

      packages.${system} = {
        inherit src;
        inherit cli cli-darwin-amd64 cli-darwin-arm64 cli-linux-arm64 cli-linux-amd64;
        inherit server;

        default = server;
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

        ami = nixos-generators.nixosGenerate {
            system = "${system}";
            format = "amazon";
            modules = [
                ({ config, ... }: {
                    boot.kernelPackages = pkgs.linuxPackages_6_6;
                    users.users.ayup = {
                        isNormalUser = true;
                        packages = [ server ];
                    };
                    amazonImage = {
                        sizeMB = "auto";
                        name = "Ayup-${version}-${config.system.nixos.label}-${system}";
                    };
                    nix.settings.experimental-features = [ "nix-command" "flakes" ];
                    networking.firewall.enable = false;
                })
                (nixpkgs.outPath + "/nixos/modules/profiles/minimal.nix")
            ];
        };
     };
    };
}
