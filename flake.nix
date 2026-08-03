{
  description = "civitai-manager — subscribe to CivitAI models/creators and auto-download new versions";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    # `github:nix-systems/default` instead of flake-utils: it is a single tiny
    # input with no transitive dependencies, and consumers that `follows` it pay
    # nothing. flake-utils only existed here for `eachDefaultSystem` + `mkApp`,
    # and `nix run` resolves `meta.mainProgram` on its own, so neither is needed.
    systems.url = "github:nix-systems/default";
  };

  outputs =
    {
      self,
      nixpkgs,
      systems,
      ...
    }:
    let
      inherit (nixpkgs) lib;

      eachSystem = lib.genAttrs (import systems);
      pkgsFor = eachSystem (system: nixpkgs.legacyPackages.${system});

      # Keep in step with the released tag. GoReleaser injects `{{ .Version }}`
      # (the tag without its leading "v") into `main.version`; this must be the
      # same string so `--version` reads the same from a Nix build and from a
      # release tarball. Bump it in the commit that cuts the tag.
      version = "0.1.103";

      # A git flake exposes `rev` when the tree is clean and `dirtyRev` when it
      # is not; a plain path (`nix build /some/dir`) exposes neither.
      commit = self.rev or (self.dirtyRev or "unknown");

      # `lastModifiedDate` is a bare "YYYYMMDDhhmmss" string; render it as the
      # RFC3339 stamp GoReleaser puts in `main.date` so both build paths print
      # the same shape.
      toRFC3339 =
        d:
        if lib.stringLength d == 14 then
          "${lib.substring 0 4 d}-${lib.substring 4 2 d}-${lib.substring 6 2 d}"
          + "T${lib.substring 8 2 d}:${lib.substring 10 2 d}:${lib.substring 12 2 d}Z"
        else
          d;
      date = toRFC3339 (self.lastModifiedDate or "unknown");

      mkCivitaiManager =
        pkgs:
        # Pinned to Go 1.25 to match go.mod (`go 1.25.0`) and CI's setup-go,
        # rather than tracking whatever `buildGoModule` currently defaults to.
        pkgs.buildGo125Module {
          pname = "civitai-manager";
          inherit version;

          # Only the files the build actually reads. This used to be
          # `src = ./.`, which swept in every untracked directory in the working
          # tree (agent worktrees under .claude/, scratch dirs, local *.db
          # files) and made the build fail — or the store path churn — for
          # reasons that had nothing to do with the code.
          src = lib.fileset.toSource {
            root = ./.;
            fileset = lib.fileset.unions [
              ./main.go
              ./go.mod
              ./go.sum
              ./internal
              ./LICENSE
              ./README.md
              (lib.fileset.maybeMissing ./cmd)
            ];
          };

          # Build (and test) only the root command. Without this, the build
          # walks src looking for anything package-shaped.
          subPackages = [ "." ];

          # SQLite is the pure-Go modernc.org/sqlite driver, so no CGO.
          env.CGO_ENABLED = "0";

          # go.mod pins github.com/civitai/cli at a pseudo-version resolved
          # through the module proxy, so `proxyVendor` is used rather than a
          # committed vendor/ tree.
          #
          # vendorHash MUST be recomputed after any go.mod/go.sum change — and
          # after any change to `proxyVendor`, which changes what is hashed.
          # Nix prints the correct value on mismatch; never guess it. CI runs
          # `nix flake check`, which is what catches the drift.
          vendorHash = "sha256-c4e1bgK1N23MvMOPdRXCUmrxkRLmXa70g7YRhQJ8I4w=";
          proxyVendor = true;

          # Ties the module fetch to go.sum so a dependency change forces the
          # vendor derivation to rebuild instead of silently reusing a stale one
          # (see the nixpkgs Go manual, `goSum`).
          goSum = ./go.sum;

          # The same -X targets GoReleaser uses (see .goreleaser.yaml), so
          # `--version` agrees between a Nix build and a release archive.
          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
            "-X main.commit=${commit}"
            "-X main.date=${date}"
          ];

          meta = {
            description = "Subscribe to CivitAI models/creators and auto-download new versions";
            longDescription = ''
              Run a ComfyUI workflow you just downloaded and let the models it
              needs fetch themselves. civitai-manager also subscribes to CivitAI
              models and creators, auto-queues downloads of new versions, and
              scans a local model library for duplicates and broken files. It is
              a single static Go binary with a loopback-only offline web UI and
              a local SQLite state file.
            '';
            homepage = "https://github.com/ZacxDev/civitai-manager";
            changelog = "https://github.com/ZacxDev/civitai-manager/releases/tag/v${version}";
            license = lib.licenses.asl20;
            mainProgram = "civitai-manager";
            platforms = lib.platforms.linux ++ lib.platforms.darwin;
          };
        };
    in
    {
      packages = eachSystem (system: rec {
        civitai-manager = mkCivitaiManager pkgsFor.${system};
        default = civitai-manager;
      });

      overlays.default = final: _prev: {
        civitai-manager = mkCivitaiManager final;
      };

      # `nix flake check` builds these. The package build is the anti-drift
      # mechanism: a stale vendorHash fails here, and CI runs it on every PR.
      checks = eachSystem (system: {
        package = self.packages.${system}.civitai-manager;
      });

      # `pkgs.nixfmt` IS the RFC-style formatter as of nixpkgs 2025-07;
      # `nixfmt-rfc-style` is now a deprecated alias that emits a warning.
      formatter = eachSystem (system: pkgsFor.${system}.nixfmt);

      devShells = eachSystem (system: {
        default = pkgsFor.${system}.mkShell {
          packages = with pkgsFor.${system}; [
            go_1_25
            gopls
            goreleaser
            actionlint
            shellcheck
            nixfmt
          ];
        };
      });
    };
}
