{
  inputs.flake-utils.url = "github:numtide/flake-utils";

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem
      (system: let
        pkgs = nixpkgs.legacyPackages.${system};
        pkg = let
          rev = "673d93f27161b345e632b86754447973410ad121";
        in pkgs.buildGo117Module rec {
          pname = "livemon";
          version = rev;
          src = pkgs.fetchFromGitHub {
            owner = "danderson";
            repo = "livemon";
            rev = rev;
            sha256 = "sha256-W2xkSH62Ayg66iVeer03CgyRSh2gzVWFPWFmY0P3owg=";
          };
          vendorSha256 = "sha256-fdDPTvhdpd4mL04OikkO2+5csyE3o8VB/Ih/m2UGaiw=";
          postInstall = ''
            sed -i -e "s#/usr/bin#$out/bin#" livemon.service
            install -D -m0444 -t $out/lib/systemd/system livemon.service
          '';
        };
      in {
        packages.livemon = pkg;
        defaultPackage = pkg;
        overlay = final: prev: {
          livemon = pkg;
        };
        nixosModule = { config, lib }: let
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
            systemd.packages = [ pkg ];
            systemd.sockets.livemon.listenStreams = [
              "${cfg.listenAddr}:${cfg.listenPort}"
              "/run/livemon/livemon.sock"
            ];
          };
        };
      });
}
