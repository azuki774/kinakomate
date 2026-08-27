{
  description = "kinakomate - restore-test runner";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/56c02bc00adcf003215cc4bd996d6efaf4cff188";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAll = nixpkgs.lib.genAttrs systems;

      # Pin Go to an exact patch version (1.26.7) by overriding the
      # nixpkgs go_1_26 source. The build process is identical across
      # 1.26.x patch releases, so this rebuilds the exact toolchain.
      mkGo = pkgs: pkgs.go_1_26.overrideAttrs (old: {
        version = "1.26.7";
        src = pkgs.fetchurl {
          url = "https://go.dev/dl/go1.26.7.src.tar.gz";
          sha256 = "sha256-DtJOrHVRBQhbif6cq8J0K5GgrXuUtZ0602SRjryJVq0=";
        };
      });
    in
    {
      devShells = forAll (system:
        let
          pkgs = import nixpkgs { inherit system; };
          go = mkGo pkgs;
        in
        {
          default = pkgs.mkShell {
            packages = [ go ];
          };
        });

      packages = forAll (system:
        let
          pkgs = import nixpkgs { inherit system; };
          go = mkGo pkgs;
        in
        {
          default = pkgs.stdenv.mkDerivation {
            pname = "kinakomate";
            version = "0.1.0";
            src = ./.;
            nativeBuildInputs = [ go ];
            buildPhase = ''
              export HOME=$TMPDIR
              export GOCACHE=$TMPDIR/go-cache
              export GOPATH=$TMPDIR/go-path
              CGO_ENABLED=0 GOOS=linux go build -trimpath -o kinakomate ./cmd/kinakomate
            '';
            installPhase = ''
              mkdir -p $out/bin
              install -m755 kinakomate $out/bin/kinakomate
            '';
          };
        });
    };
}
