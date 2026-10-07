{ lib, buildGoModule, buildNpmPackage, nodejs_22, bash }:
let
  version = "0.2.0";
  src = lib.cleanSource ../.;

  # The dashboard is embedded into the binary at build time (web/embed.go).
  dashboard = buildNpmPackage {
    pname = "nebula-dashboard";
    inherit version;
    src = "${src}/web";
    nodejs = nodejs_22;
    npmDepsHash = "sha256-9ys+lqXoJAqNE8d0ELlcw+jJd+hu9/6s7fOWynwkXUM=";
    # `npm run build` also writes dist/.gitkeep, which embed.go needs.
    installPhase = ''
      runHook preInstall
      cp -r dist $out
      runHook postInstall
    '';
  };
in
buildGoModule {
  pname = "nebula";
  inherit version src;
  # Standard library only.
  vendorHash = null;

  subPackages = [ "cmd/nebula" ];
  ldflags = [ "-s" "-w" "-X main.version=${version}" ];
  env.CGO_ENABLED = 0;

  preBuild = ''
    rm -rf web/dist
    cp -r ${dashboard} web/dist
    chmod -R u+w web/dist
  '';

  # Companion binaries the server hands to devices (`nebula update`). The
  # backup script, as in Claustra, is installed from its own store path, so
  # its shebang is rewritten here rather than by patchShebangs.
  postInstall = ''
    for target in linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
      os=''${target%/*} arch=''${target#*/} ext=
      [ "$os" = windows ] && ext=.exe
      GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=${version}" \
        -o $out/share/nebula/downloads/nebula-$os-$arch$ext ./cmd/nebula
    done
    install -Dm755 ${../scripts/backup.sh} $out/bin/nebula-backup
    substituteInPlace $out/bin/nebula-backup --replace-fail '#!/usr/bin/env bash' '#!${bash}/bin/bash'
  '';

  doCheck = true;
  meta = {
    description = "Claude Code session archive and account switching";
    homepage = "https://github.com/olivermarcusson/nebula";
    license = lib.licenses.mit;
    mainProgram = "nebula";
    platforms = lib.platforms.linux;
  };
}
