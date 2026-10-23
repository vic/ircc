{ pkgs ? import <nixpkgs> { } }:
pkgs.buildGoModule {
  pname = "ircc";
  version = "0.1.0";
  src = pkgs.lib.fileset.toSource {
    root = ./.;
    fileset = pkgs.lib.fileset.unions [ ./cmd ./internal ./go.mod ./go.sum ];
  };
  vendorHash = "sha256-Im0gy9h9JYcQuD5gktHmGb+yNEuiDmHu4yIyU/y2hH8=";
  env.CGO_ENABLED = 0;
  subPackages = [ "cmd/ircc" ];
}
