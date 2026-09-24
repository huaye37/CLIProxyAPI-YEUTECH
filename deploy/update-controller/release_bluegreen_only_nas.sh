#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PASSWORD_FILE=/Users/fangjialiang/Documents/家庭网络中枢项目/04_工具与运维/工具脚本与网络配置/服务器密码.rtf
STAGE=$(ssh yeutech-nas 'mktemp -d /tmp/yeutech-bluegreen-release-XXXXXX')

cleanup() {
  if [ -n "${NAS_PASSWORD:-}" ]; then
    printf '%s\n' "$NAS_PASSWORD" | ssh yeutech-nas "sudo -S -p '' rm -r -- '$STAGE'" >/dev/null 2>&1 || true
  else
    ssh yeutech-nas "rm -r -- '$STAGE'" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

scp -O "$ROOT/bluegreen.py" "$ROOT/release_bluegreen_only.py" "yeutech-nas:$STAGE/"
CREDENTIAL=$(textutil -convert txt -stdout "$PASSWORD_FILE" | tr -d '\r' | sed -n -E 's/^nas[：:](.+)$/\1/p' | head -n 1 | sed -E 's/^[[:space:]]+//; s/[[:space:]]+$//')
NAS_PASSWORD=$(printf '%s\n' "$CREDENTIAL" | awk '{print $2}')
test -n "$NAS_PASSWORD"
printf '%s\n' "$NAS_PASSWORD" | ssh yeutech-nas "sudo -S -p '' /usr/bin/python3 '$STAGE/release_bluegreen_only.py' '$STAGE'"
unset NAS_PASSWORD CREDENTIAL
