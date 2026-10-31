#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  cat <<'EOF'
Usage: ./restart.sh [--config-only | --postgresql]
  (no option)    Deploy webapp and restart the currently active language service.
  --config-only Copy system configuration and run daemon-reload; do not restart.
  --postgresql  Copy system configuration and restart PostgreSQL on asia2.
Run as isucon in the instance checkout, after git pull --ff-only.
EOF
}

mode=app
case "${1:-}" in
  '') ;;
  --config-only) mode=config ;;
  --postgresql) mode=db ;;
  -h|--help) usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac
[[ $# -le 1 ]] || { usage >&2; exit 2; }

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
live_webapp=/home/isucon/webapp
export PATH="/home/isucon/local/go/bin:/home/isucon/.cargo/bin:/home/isucon/local/node/bin:/home/isucon/local/perl/bin:/home/isucon/local/php/bin:/home/isucon/local/ruby/bin:/home/isucon/local/uv:$PATH"

[[ $(id -un) == isucon ]] || { echo 'Run as isucon, not root.' >&2; exit 1; }
[[ -d "$live_webapp" && ! -L "$live_webapp" ]] || {
  echo "Expected a real directory at $live_webapp" >&2; exit 1;
}
exec 9>/home/isucon/.isucon2026-restart.lock
flock -n 9 || { echo 'Another deployment is running.' >&2; exit 1; }
sudo -n true

is_db=false
if systemctl is-active --quiet postgresql@16-main.service; then is_db=true; fi
language=''
if [[ $mode == app ]]; then
  for candidate in go nodejs perl php python ruby rust; do
    if systemctl is-active --quiet "isureal-$candidate.service"; then
      [[ -z $language ]] || { echo 'Multiple language services are active.' >&2; exit 1; }
      language=$candidate
    fi
  done
  [[ -n $language ]] || {
    echo 'No app service is active. On asia2 use --config-only or --postgresql.' >&2
    exit 1
  }
elif [[ $mode == db && $is_db != true ]]; then
  echo '--postgresql requires the running asia2 PostgreSQL instance.' >&2; exit 1
fi

build_dir=$(mktemp -d /tmp/isucon2026-build.XXXXXXXX)
backup_dir=''
cleanup() {
  result=$?
  rm -rf -- "$build_dir"
  if [[ $result -ne 0 && -n $backup_dir ]]; then
    echo "Deployment failed; original system files are in $backup_dir" >&2
  fi
}
trap cleanup EXIT

# Build before replacing the running binary. install below uses an atomic rename.
case "$language" in
  go) (cd "$repo_root/webapp/go" && go build -o "$build_dir/isureal" ./cmd/isureal) ;;
  rust)
    (cd "$repo_root/webapp/rust" && cargo build --release --locked)
    cp "$repo_root/webapp/rust/target/release/isureal" "$build_dir/isureal"
    ;;
esac

# Keep the original paths, owners and modes for recovery.
backup_dir="/var/backups/isucon2026/$(date -u +%Y%m%dT%H%M%SZ)-$$"
sudo install -d -m 0700 "$backup_dir"
copy_config() {
  local source=$1 destination=$2 owner=${3:-root} group=${4:-root} mode=${5:-0644}
  sudo cp --archive --parents "$destination" "$backup_dir/"
  sudo install -o "$owner" -g "$group" -m "$mode" "$source" "$destination"
}
for candidate in go nodejs perl php python ruby rust; do
  path="etc/systemd/system/isureal-$candidate.service"
  copy_config "$repo_root/system/common/$path" "/$path"
done
for path in etc/nginx/nginx.conf etc/nginx/sites-available/isureal.conf etc/nginx/sites-available/isureal-php.conf; do
  copy_config "$repo_root/system/common/$path" "/$path"
done
if [[ $is_db == true ]]; then
  pg_root=etc/postgresql/16/main
  for name in postgresql.conf conf.d/isucon.conf; do
    copy_config "$repo_root/system/asia2/$pg_root/$name" "/$pg_root/$name" postgres postgres
  done
  for name in pg_hba.conf pg_ident.conf; do
    copy_config "$repo_root/system/asia2/$pg_root/$name" "/$pg_root/$name" postgres postgres 0640
  done
fi

sudo systemd-analyze verify "$repo_root"/system/common/etc/systemd/system/*.service
sudo nginx -t
if [[ $is_db == true ]]; then
  sudo -u postgres /usr/lib/postgresql/16/bin/postgres \
    -D /var/lib/postgresql/16/main -c config_file=/etc/postgresql/16/main/postgresql.conf -C port >/dev/null
  errors=$(sudo -u postgres psql -X -A -t -v ON_ERROR_STOP=1 -c \
    "SELECT error FROM pg_file_settings WHERE error IS NOT NULL UNION ALL SELECT error FROM pg_hba_file_rules WHERE error IS NOT NULL")
  [[ -z $errors ]] || { echo "$errors" >&2; exit 1; }
fi
sudo systemctl daemon-reload
echo "System configuration copied; backup: $backup_dir"

if [[ $mode == config ]]; then
  echo 'Configuration installed. Running services have not been restarted or reloaded.'
  exit 0
fi
if [[ $mode == db ]]; then
  sudo systemctl restart postgresql@16-main.service
  sudo systemctl is-active --quiet postgresql@16-main.service
  sudo -u postgres psql -X -v ON_ERROR_STOP=1 -c 'SELECT 1' >/dev/null
  echo 'PostgreSQL restarted.'
  exit 0
fi

# Protect instance-installed dependencies, binaries and runtime files from --delete.
rsync --archive --checksum --delete \
  --exclude=node_modules/ --exclude=vendor/ --exclude=.venv/ \
  --exclude=local/ --exclude=target/ --exclude=.bundle/ --exclude=__pycache__/ \
  --exclude=/go/isureal --exclude=/rust/isureal \
  --exclude='*.log' --exclude='*.pid' --exclude='*.sock' \
  "$repo_root/webapp/" "$live_webapp/"

case "$language" in
  go|rust)
    install -m 0755 "$build_dir/isureal" "$live_webapp/$language/isureal.next"
    mv -f "$live_webapp/$language/isureal.next" "$live_webapp/$language/isureal"
    ;;
  nodejs) (cd "$live_webapp/nodejs" && npm ci --omit=dev) ;;
  perl|php|python|ruby) make -C "$live_webapp/$language" install ;;
esac

sudo systemctl restart "isureal-$language.service"
sudo systemctl is-active --quiet "isureal-$language.service"
sudo systemctl reload nginx.service
for ((attempt=0; attempt<20; attempt++)); do
  status=$(curl --silent --output /dev/null --write-out '%{http_code}' --max-time 3 http://127.0.0.1/api/me || true)
  if [[ $status == 401 ]]; then
    echo "isureal-$language restarted; HTTP /api/me returns 401 as expected."
    exit 0
  fi
  sleep 0.5
done
echo 'App did not become ready; inspect journalctl for the active service.' >&2
exit 1
