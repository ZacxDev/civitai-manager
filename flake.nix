{
  description = "civitai-manager — subscribe to CivitAI models/creators and auto-download new versions";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    { nixpkgs, flake-utils, ... }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs { inherit system; };
        version = "dev";
        civitai-manager = pkgs.buildGoModule {
          pname = "civitai-manager";
          inherit version;
          src = ./.;

          # SQLite is the pure-Go modernc.org/sqlite driver, so no CGO.
          env.CGO_ENABLED = "0";

          # go.mod pins github.com/civitai/cli at a pseudo-version; the Go proxy
          # resolves it, so proxyVendor works. Update vendorHash after any
          # go.mod/go.sum change (Nix reports the correct hash on mismatch).
          vendorHash = "sha256-oj94jK5ac99nk/iqiyS8oRCS611VUz2IkD0Tem22V60=";
          proxyVendor = true;

          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
          ];

          # Skips the build-tagged live integration suite by default; ordinary
          # unit tests run at build time.
          meta = with pkgs.lib; {
            description = "Subscribe to CivitAI models/creators and auto-download new versions";
            homepage = "https://github.com/ZacxDev/civitai-manager";
            license = licenses.asl20;
            mainProgram = "civitai-manager";
          };
        };
      in
      {
        packages.default = civitai-manager;
        apps.default = flake-utils.lib.mkApp { drv = civitai-manager; };

        devShells.default = pkgs.mkShell {
          packages = [
            pkgs.go_1_25
            pkgs.goreleaser
            pkgs.actionlint
          ];
        };
      }
    );
}
