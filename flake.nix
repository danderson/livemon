{
  inputs.flake-utils.url = "github:numtide/flake-utils";

  outputs = { self, nixpkgs, flake-utils }: let
    mkPkg = pkgs: pkgs.buildGo117Module rec {
      name = "livemon";
      src = ./.;
      vendorSha256 = "sha256-fdDPTvhdpd4mL04OikkO2+5csyE3o8VB/Ih/m2UGaiw=";
      postInstall = ''
        sed -i -e "s#/usr/bin#$out/bin#" livemon.service
        install -D -m0444 -t $out/lib/systemd/system livemon.service
      '';
    };
  in
    (flake-utils.lib.eachDefaultSystem
      (system: let
        pkgs = nixpkgs.legacyPackages.${system};
        pkg = mkPkg pkgs;
      in {
        packages.livemon = pkg;
        defaultPackage = pkg;
      })) // {
        nixosModules.livemon = { config, lib, pkgs, ... }: let
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

          config = lib.mkIf cfg.enable {
            systemd.packages = [ (mkPkg pkgs) ];
            systemd.sockets.livemon = {
              enable = true;
              listenStreams = [
                "${cfg.listenAddr}:${builtins.toString cfg.listenPort}"
                "/run/livemon/livemon.sock"
              ];
            };
          };
        };
      };
}
