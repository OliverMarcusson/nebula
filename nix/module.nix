{ self }:
{ config, lib, pkgs, ... }:
let
  inherit (lib) mkEnableOption mkIf mkOption types;
  cfg = config.services.nebula;
  stateDir = "/var/lib/nebula";
  credentials = "${stateDir}/credentials";
in {
  options.services.nebula = {
    enable = mkEnableOption "Nebula session archive server";
    package = mkOption { type = types.package; default = self.packages.${pkgs.system}.default; };
    domain = mkOption { type = types.str; default = "nebula.marcusson.dev"; description = "Public hostname, served over HTTPS by a reverse proxy."; };
    listenAddress = mkOption { type = types.str; default = "127.0.0.1:13005"; };
    owner = mkOption { type = types.str; description = "Archive owner whose device token is created on first start."; };
    caddy = mkOption { type = types.bool; default = true; description = "Serve the domain through the NixOS Caddy service."; };
    claustra = {
      enable = mkEnableOption "dashboard sign-in through Claustra (OpenID Connect)";
      issuer = mkOption { type = types.str; default = "https://claustra.marcusson.dev"; };
      clientId = mkOption { type = types.str; default = "nebula"; };
      emails = mkOption { type = types.listOf types.str; default = [ ]; description = "Verified emails allowed to sign in, checked in addition to Claustra's own allowlist."; };
    };
    environmentFile = mkOption { type = types.nullOr types.str; default = null; description = "Root-managed environment file with NEBULA_OIDC_CLIENT_SECRET; do not use a Nix store path."; };
    backup = {
      enable = mkEnableOption "daily encrypted Nebula backups";
      directory = mkOption { type = types.str; default = "/var/backup/nebula"; };
      ageRecipient = mkOption { type = types.str; default = ""; description = "Public age recipient used to encrypt backups."; };
    };
  };

  config = mkIf cfg.enable {
    assertions = [
      { assertion = builtins.match "[A-Za-z0-9._-]+" cfg.owner != null; message = "services.nebula.owner must be a plain name"; }
      { assertion = !cfg.backup.enable || cfg.backup.ageRecipient != ""; message = "services.nebula.backup.ageRecipient is required when backups are enabled"; }
      { assertion = !cfg.claustra.enable || cfg.environmentFile != null; message = "services.nebula.environmentFile with NEBULA_OIDC_CLIENT_SECRET is required for Claustra sign-in"; }
    ];

    users.groups.nebula = { };
    users.users.nebula = { isSystemUser = true; group = "nebula"; home = stateDir; };

    services.caddy = mkIf cfg.caddy {
      enable = true;
      # Session chunks are at most 16 MiB per request.
      virtualHosts.${cfg.domain}.extraConfig = ''
        request_body {
          max_size 20MB
        }
        reverse_proxy ${cfg.listenAddress}
      '';
    };

    systemd.services.nebula = {
      description = "Nebula session archive";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];
      environment = lib.optionalAttrs cfg.claustra.enable {
        NEBULA_PUBLIC_URL = "https://${cfg.domain}";
        NEBULA_OIDC_ISSUER = cfg.claustra.issuer;
        NEBULA_OIDC_CLIENT_ID = cfg.claustra.clientId;
        NEBULA_OIDC_OWNER = cfg.owner;
        NEBULA_OIDC_EMAILS = lib.concatStringsSep "," cfg.claustra.emails;
      };
      # First start: create the owner's credentials. The device token is then
      # read once by the owner (sudo cat ${credentials}/device.token).
      preStart = "test -e ${credentials} || ${cfg.package}/bin/nebula init --user ${lib.escapeShellArg cfg.owner} --dir ${credentials}";
      serviceConfig = {
        Type = "simple";
        User = "nebula";
        Group = "nebula";
        ExecStart = "${cfg.package}/bin/nebula serve --listen ${cfg.listenAddress} --data ${stateDir}/archive --auth ${credentials}/users.json";
        EnvironmentFile = lib.optional (cfg.environmentFile != null) cfg.environmentFile;
        StateDirectory = "nebula";
        StateDirectoryMode = "0700";
        Restart = "on-failure";
        RestartSec = "2s";
        UMask = "0077";
        NoNewPrivileges = true;
        PrivateTmp = true;
        PrivateDevices = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        ProtectClock = true;
        ProtectHostname = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectKernelLogs = true;
        ProtectControlGroups = true;
        ProtectProc = "invisible";
        RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" ];
        RestrictNamespaces = true;
        RestrictRealtime = true;
        RestrictSUIDSGID = true;
        LockPersonality = true;
        MemoryDenyWriteExecute = true;
        CapabilityBoundingSet = "";
        AmbientCapabilities = "";
        SystemCallArchitectures = "native";
        SystemCallFilter = [ "@system-service" "~@privileged" "~@resources" ];
      };
    };

    # As for Claustra: ProtectSystem=strict needs ReadWritePaths to exist
    # before the unit's namespace is set up, so tmpfiles creates it.
    systemd.tmpfiles.rules = lib.optional cfg.backup.enable
      "d ${cfg.backup.directory} 0700 nebula nebula -";

    systemd.services.nebula-backup = mkIf cfg.backup.enable {
      description = "Encrypted Nebula backup";
      after = [ "nebula.service" ];
      path = [ pkgs.bash pkgs.age pkgs.gnutar pkgs.coreutils pkgs.findutils ];
      environment = {
        NEBULA_BACKUP_DIR = cfg.backup.directory;
        NEBULA_STATE_DIR = stateDir;
        NEBULA_BACKUP_AGE_RECIPIENT = cfg.backup.ageRecipient;
      };
      serviceConfig = {
        Type = "oneshot";
        User = "nebula";
        Group = "nebula";
        ExecStart = "${cfg.package}/bin/nebula-backup";
        ReadWritePaths = [ cfg.backup.directory ];
        UMask = "0077";
        NoNewPrivileges = true;
        PrivateTmp = true;
        ProtectSystem = "strict";
        ProtectHome = true;
      };
    };
    systemd.timers.nebula-backup = mkIf cfg.backup.enable {
      wantedBy = [ "timers.target" ];
      timerConfig = { OnCalendar = "daily"; Persistent = true; RandomizedDelaySec = "30m"; };
    };
  };
}
