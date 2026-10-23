{
  description = "ircc: a local IRC-like chat for your Claude Code sessions";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { nixpkgs, ... }:
    let
      # Linux only: ircc reads /proc and SO_PEERCRED.
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAll (pkgs: {
        default = import ./default.nix { inherit pkgs; };
      });
      devShells = forAll (pkgs: {
        default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.gcc
            pkgs.just
          ];
        };
      });
    };
}
