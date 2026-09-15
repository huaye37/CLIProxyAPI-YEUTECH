#!/bin/sh
# Publish an already-reviewed Git commit to the NAS proxy with automatic rollback.
# This script is intentionally run from a clean local checkout, never on the NAS.
set -eu

REPO_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
NAS_HOST=${NAS_HOST:-yeutech-nas}
NAS_ROOT=${NAS_ROOT:-/volume1/docker/novel-ai-proxy}
BRANCH=${BRANCH:-yeutech-capability-v15}
PASSWORD_FILE=${PASSWORD_FILE:-/Users/fangjialiang/Documents/家庭网络中枢项目/04_工具与运维/工具脚本与网络配置/服务器密码.rtf}

die() { printf '%s\n' "ERROR: $*" >&2; exit 1; }

command -v git >/dev/null 2>&1 || die "git is required"
command -v scp >/dev/null 2>&1 || die "scp is required"
command -v ssh >/dev/null 2>&1 || die "ssh is required"
command -v tar >/dev/null 2>&1 || die "tar is required"
command -v textutil >/dev/null 2>&1 || die "macOS textutil is required to read the NAS sudo credential"

cd "$REPO_DIR"
[ -z "$(git status --porcelain)" ] || die "working tree is not clean; commit or stash changes before release"
# The workstation may retain a stale GitHub proxy setting after the proxy app
# has stopped. Bypass that host-specific setting without changing global Git.
git -c 'http.https://github.com.proxy=' -c 'https.https://github.com.proxy=' fetch origin --prune
COMMIT=$(git rev-parse HEAD)
git merge-base --is-ancestor "$COMMIT" "origin/$BRANCH" || die "HEAD is not published to origin/$BRANCH"

STAMP=$(date '+%Y%m%d-%H%M%S')
SHORT_COMMIT=$(git rev-parse --short=12 HEAD)
RELEASE="${STAMP}-${SHORT_COMMIT}"
IMAGE="cli-proxy-api:yeutech-${SHORT_COMMIT}"
REMOTE_STAGE="$NAS_ROOT/releases/$RELEASE"

[ -r "$PASSWORD_FILE" ] || die "NAS credential file is not readable"
CREDENTIAL_RECORD=$(textutil -convert txt -stdout "$PASSWORD_FILE" | tr -d '\r' | sed -n -E 's/^nas[：:](.+)$/\1/p' | head -n 1)
CREDENTIAL_RECORD=$(printf '%s' "$CREDENTIAL_RECORD" | sed -E 's/^[[:space:]]+//; s/[[:space:]]+$//')
[ -n "$CREDENTIAL_RECORD" ] || die "NAS credential record is missing"
case "$CREDENTIAL_RECORD" in
  */*)
    NAS_SSH_USER=${CREDENTIAL_RECORD%%/*}
    PASSWORD=${CREDENTIAL_RECORD#*/}
    ;;
  *[[:space:]]*)
    NAS_SSH_USER=$(printf '%s\n' "$CREDENTIAL_RECORD" | awk '{print $1}')
    PASSWORD=$(printf '%s\n' "$CREDENTIAL_RECORD" | awk '{print $2}')
    ;;
  *) die "NAS credential record must use user/password or user password format" ;;
esac
[ -n "$NAS_SSH_USER" ] && [ -n "$PASSWORD" ] || die "NAS credential record is incomplete"
PASSWORD=${NAS_SUDO_PASSWORD:-$PASSWORD}
NAS_TARGET="$NAS_SSH_USER@$NAS_HOST"

printf 'Preparing NAS release %s from %s\n' "$RELEASE" "$COMMIT"
ssh "$NAS_TARGET" "rm -rf '$REMOTE_STAGE' && mkdir -p '$REMOTE_STAGE/source'"

# Runtime configuration, OAuth credentials, and logs do not travel with source.
# Synology's rsync-over-SSH is not reliable on this NAS. Legacy SCP and tar
# retain the key-based SSH path and do not use its broken remote rsync mode.
tar -C "$REPO_DIR" \
  --exclude='./.git' --exclude='./.DS_Store' --exclude='./auths' \
  --exclude='./logs' --exclude='./plugins' \
  -cf - . | ssh "$NAS_TARGET" "tar -xf - -C '$REMOTE_STAGE/source'"

DEPLOY_SCRIPT=$(mktemp "${TMPDIR:-/tmp}/cliproxy-nas-deploy.XXXXXX")
trap 'rm -f "$DEPLOY_SCRIPT"' EXIT HUP INT TERM
cat > "$DEPLOY_SCRIPT" <<'REMOTE'
#!/bin/sh
set -eu
nas_root=$1
release=$2
image=$3
remote_stage=$4
PATH=/var/packages/ContainerManager/target/usr/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin
export PATH
compose="$nas_root/compose.yaml"
backup_dir="$nas_root/backups/one-click-$release"
old_image=$(awk '/^[[:space:]]*image:[[:space:]]*/ { print $2; exit }' "$compose")
[ -n "$old_image" ] || { echo 'Cannot determine current image' >&2; exit 1; }

mkdir -p "$backup_dir"
cp -p "$compose" "$backup_dir/compose.yaml"
printf '%s\n' "$old_image" > "$backup_dir/previous-image.txt"
printf '%s\n' "$release" > "$backup_dir/release.txt"

docker build \
  --build-arg "VERSION=yeutech-$release" \
  --build-arg "COMMIT=$release" \
  --build-arg "BUILD_DATE=$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
  -t "$image" "$remote_stage/source"

compose_tmp="$backup_dir/compose.yaml.next"
sed "s|^[[:space:]]*image:.*|    image: $image|" "$compose" > "$compose_tmp"
cp "$compose_tmp" "$compose"

if docker compose -f "$compose" up -d --no-build --force-recreate; then
  attempt=0
  while [ "$attempt" -lt 12 ]; do
    status=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' novel-ai-proxy 2>/dev/null || true)
    if [ "$status" = healthy ]; then
      echo "RELEASE_OK release=$release image=$image backup=$backup_dir"
      exit 0
    fi
    attempt=$((attempt + 1))
    sleep 5
  done
fi

echo "Release failed; restoring $old_image" >&2
cp "$backup_dir/compose.yaml" "$compose"
docker compose -f "$compose" up -d --no-build --force-recreate
exit 1
REMOTE
chmod 700 "$DEPLOY_SCRIPT"
scp -O "$DEPLOY_SCRIPT" "$NAS_TARGET:$REMOTE_STAGE/deploy.sh"

printf 'Building and switching the NAS proxy. It will briefly restart only after the new image builds.\n'
if ! printf '%s\n' "$PASSWORD" | ssh "$NAS_TARGET" "sudo -S -p '' /bin/sh '$REMOTE_STAGE/deploy.sh' '$NAS_ROOT' '$RELEASE' '$IMAGE' '$REMOTE_STAGE'"; then
  die "NAS release failed; the prior compose configuration was restored"
fi
printf 'Release completed: %s\n' "$RELEASE"
