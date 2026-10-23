{ pkgs ? import <nixpkgs> { } }:
pkgs.buildGoModule {
  pname = "ircc";
  version = "0.1.0";
  src = pkgs.lib.fileset.toSource {
    root = ./.;
    fileset = pkgs.lib.fileset.unions [ ./cmd ./internal ./go.mod ./go.sum ];
  };
  vendorHash = "sha256-RYxmQayATgIm/4zIyFs6nozTQdTqX4AwfqbKcP0LNHM=";
  env.CGO_ENABLED = 0;
  subPackages = [ "cmd/ircc" ];
}
