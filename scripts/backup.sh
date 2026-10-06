#!/usr/bin/env bash
# Encrypted Nebula backup: the archive, accounts, and credentials in one
# age-encrypted tar, keeping the newest seven.
#
# Restore: stop nebula, decrypt and extract into /var/lib/nebula, remove
# archive/<owner>/index.json (rebuilt from manifests on start), start nebula.
set -euo pipefail
umask 077

backup_dir=${NEBULA_BACKUP_DIR:?NEBULA_BACKUP_DIR is required}
state_dir=${NEBULA_STATE_DIR:?NEBULA_STATE_DIR is required}
age_recipient=${NEBULA_BACKUP_AGE_RECIPIENT:?NEBULA_BACKUP_AGE_RECIPIENT is required}

case "$backup_dir" in ""|/|.|..) echo "refusing unsafe backup directory" >&2; exit 64;; esac
mkdir -p -- "$backup_dir"
partial="$backup_dir/.nebula-backup-$$.part"
trap 'rm -f -- "$partial"' EXIT

# Chunks are immutable and manifests are written atomically, so a live copy is
# consistent up to the index, which a restore rebuilds.
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
tar -C "$state_dir" --exclude='.tmp-*' -cf - . | age --recipient "$age_recipient" --output "$partial"
mv -- "$partial" "$backup_dir/nebula-$timestamp.tar.age"

mapfile -t backups < <(find "$backup_dir" -maxdepth 1 -type f -name 'nebula-*.tar.age' -printf '%T@ %p\n' | sort -rn | cut -d' ' -f2-)
for ((i=7; i<${#backups[@]}; i++)); do rm -f -- "${backups[$i]}"; done
