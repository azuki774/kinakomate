{
  description = "kinakomate - azure key restore test runner";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }: let
    systems = [ "x86_64-linux" "aarch64-linux" ];
    forAllSystems = nixpkgs.lib.genAttrs systems;
  in {
    packages = forAllSystems (system: let
      pkgs = import nixpkgs { inherit system; };
      buildGoModule = pkgs.buildGoModule.override { go = pkgs.go_1_27; };
    in {
      default = buildGoModule {
        pname = "kinakomate";
        version = "0.1.0";
        src = ./.;
        subPackages = [ "./cmd/kinakomate" ];
        vendorHash = null;
        meta.mainProgram = "kinakomate";
      };
    });

    apps = forAllSystems (system: {
      default = {
        type = "app";
        program = "${self.packages.${system}.default}/bin/kinakomate";
      };
    });

    devShells = forAllSystems (system: let
      pkgs = import nixpkgs { inherit system; };
    in {
      default = pkgs.mkShell {
        buildInputs = [ pkgs.go_1_27 ];
      };
    });
  };
}
