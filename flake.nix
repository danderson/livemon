{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  inputs.flake-utils.url = "github:numtide/flake-utils";

  outputs = { self, nixpkgs, flake-utils }: let
    livemon = pkgs: pkgs.buildGo122Module rec {
      name = "livemon";
      src = ./.;
      vendorHash = "sha256-5e7Q83JYtPkIqJDtQ1zE81EeYkD+FOOCfkSGK8IG0VE=";
      postInstall = ''
         sed -i -e "s#/usr/bin#$out/bin#" livemon.service
         install -D -m0444 -t $out/lib/systemd/system livemon.service
      '';
    };

    perSystem = flake-utils.lib.eachDefaultSystem (system: let
      pkgs = nixpkgs.legacyPackages.${system};
      pkg = livemon pkgs;
    in {
      packages.livemon = pkg;
      defaultPackage = pkg;
    });

    nixos = { config, lib, pkgs, ... }: let
      cfg = config.services.livemon;
    in {
      options.services.livemon = {
        enable = lib.mkOption {
          type = lib.types.bool;
          default = false;
        };
        listenAddr = lib.mkOption {
          type = lib.types.str;
          default = "[::1]";
        };
        listenPort = lib.mkOption {
          type = lib.types.port;
          default = 9843;
        };
        monitoredServices = lib.mkOption {
          type = lib.types.attrsOf lib.types.str;
          default = {};
        };
      };

      config = let
        pkg = livemon pkgs;
      in lib.mkIf cfg.enable  {
        environment.systemPackages = [ pkg ];
        systemd.packages = [ pkg ];
        systemd.services.livemon.serviceConfig.ExecStart = [
          ""
          "${pkg}/bin/livemon daemon --addr=${cfg.listenAddr}:${builtins.toString cfg.listenPort} --unix=/run/livemon/livemon.sock"
        ];
      };
    };
  in
    perSystem // {
      nixosModules.livemon = nixos;
      nixosModules.default = nixos;
    };
}
