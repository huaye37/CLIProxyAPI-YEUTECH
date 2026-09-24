#!/usr/bin/env python3
"""Restricted update agent for the single production CLIProxyAPI container."""

from __future__ import annotations

import hmac
import fcntl
import json
import os
import re
import shutil
import subprocess
import tarfile
import threading
import time
import urllib.request
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Optional

import bluegreen

HOST = "127.0.0.1"
PORT = 18321
ROOT = Path("/volume1/docker/novel-ai-proxy")
COMPOSE = ROOT / "compose.yaml"
CONFIG = ROOT / "config"
AUTH = ROOT / "auth"
BACKUPS = ROOT / "update-backups"
STATE_FILE = ROOT / "update-state.json"
TOKEN_FILE = ROOT / "update-agent.key"
DOCKER = "/usr/local/bin/docker"
COMPOSE_BIN = "/usr/local/bin/docker-compose"
CONTAINER = "novel-ai-proxy"
IMAGE_REPOSITORY = "eceasy/cli-proxy-api"
YEUTECH_REPOSITORY = "https://github.com/huaye37/CLIProxyAPI-YEUTECH.git"
YEUTECH_BRANCH = "yeutech-capability-v15"
UPSTREAM_REPOSITORY = "https://github.com/router-for-me/CLIProxyAPI.git"
RELEASES = ROOT / "releases"
RELEASE_API = "https://api.github.com/repos/router-for-me/CLIProxyAPI/releases/latest"
VERSION_RE = re.compile(r"^v7\.\d+\.\d+$")
IMAGE_RE = re.compile(r"^\s*image:\s*(\S+)\s*$", re.MULTILINE)
YEUTECH_CAPABILITIES_VARIANT = "yeutech-capabilities"

state_lock = threading.Lock()
job_lock = threading.Lock()


def run(*args: str, timeout: int = 1200) -> str:
    result = subprocess.run(args, text=True, capture_output=True, timeout=timeout)
    if result.returncode:
        detail = (result.stderr or result.stdout).strip().splitlines()
        raise RuntimeError(detail[-1][:300] if detail else "命令执行失败")
    return result.stdout.strip()


def read_state() -> dict:
    with state_lock:
        try:
            value = json.loads(STATE_FILE.read_text())
            return value if isinstance(value, dict) else {}
        except (FileNotFoundError, json.JSONDecodeError):
            return {}


def write_state(**changes) -> dict:
    with state_lock:
        try:
            value = json.loads(STATE_FILE.read_text())
        except (FileNotFoundError, json.JSONDecodeError):
            value = {}
        value.update(changes, updatedAt=int(time.time()))
        temporary = STATE_FILE.with_suffix(".tmp")
        temporary.write_text(json.dumps(value, ensure_ascii=False, separators=(",", ":")))
        os.chmod(temporary, 0o600)
        temporary.replace(STATE_FILE)
        return value


def current_image() -> str:
    if bluegreen.STATE_FILE.exists():
        return str(bluegreen.production_state().get("image", ""))
    return run(DOCKER, "inspect", CONTAINER, "--format", "{{.Config.Image}}", timeout=30)


def image_version(image: str) -> str:
    yeutech = re.search(r":yeutech-([0-9a-f]{7,40})$", image)
    if yeutech:
        return "YEUTECH " + yeutech.group(1)[:12]
    yeutech_variant = re.search(r":yeutech-([A-Za-z0-9][A-Za-z0-9._-]*)$", image)
    if yeutech_variant:
        return "YEUTECH " + yeutech_variant.group(1)
    match = re.search(r":(v7\.\d+\.\d+)(?:-[A-Za-z0-9._-]+)?(?:@|$)", image)
    return match.group(1) if match else "unknown"


def official_update_block_reason(image: str) -> Optional[str]:
    """Prevent an upstream image from silently removing YEUTECH's API contract."""
    if YEUTECH_CAPABILITIES_VARIANT not in image.lower():
        return None
    return (
        "当前运行 YEUTECH 模型能力目录定制版，禁止直接替换为官方镜像，"
        "否则会丢失 /v1/model-capabilities。请先合并新上游版本、构建并验证 "
        "YEUTECH capabilities 镜像，再人工切换。"
    )


def is_yeutech_image(image: str) -> bool:
    """Only the maintained YEUTECH branch may replace a customized image."""
    return not image.startswith(IMAGE_REPOSITORY + ":")


def yeutech_commit(image: str) -> str:
    match = re.search(r":yeutech-([0-9a-f]{7,40})$", image)
    return match.group(1) if match else ""


def yeutech_branch_head() -> str:
    output = run("/usr/bin/git", "ls-remote", YEUTECH_REPOSITORY, "refs/heads/" + YEUTECH_BRANCH, timeout=30)
    return output.split()[0] if output else ""


def container_health() -> str:
    try:
        if bluegreen.STATE_FILE.exists():
            status = bluegreen.gateway_status()
            return "healthy" if status.get("ok") else "unhealthy"
        value = run(DOCKER, "inspect", CONTAINER, "--format", "{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}", timeout=30)
        return value or "unknown"
    except Exception:
        return "missing"


def public_status() -> dict:
    saved = read_state()
    try:
        image = current_image()
    except Exception:
        image = ""
    previous = saved.get("previousImage", "")
    yeutech_build_required = is_yeutech_image(image)
    current_commit = yeutech_commit(image)
    branch_commit = str(saved.get("branchCommit", ""))
    blocked_reason = official_update_block_reason(image)
    ready = saved.get('upstreamIncluded') is True if yeutech_build_required else True
    if yeutech_build_required and not ready:
        blocked_reason = saved.get('integrationMessage') or '请检查更新以核对维护分支是否已合并官方版本'
    install_ready = saved.get('installReady') is True if yeutech_build_required else True
    if yeutech_build_required and ready and not install_ready:
        blocked_reason = saved.get('installationMessage') or '请重新检查发布槽和入口配置'
    return {
        "controllerVersion": "20260925-safe-update-v1",
        "updateMode": "reviewed-branch-only",
        "updateReady": ready,
        "installReady": install_ready,
        "upstreamBehindCommits": saved.get('upstreamBehindCommits'),
        "upstreamIntegratedVersion": saved.get("upstreamIntegratedVersion"),
        "branchCommit": branch_commit,
        "currentVersion": image_version(image),
        "currentImage": image,
        "latestVersion": saved.get("latestVersion"),
        "releaseUrl": saved.get("releaseUrl"),
        "releaseNotes": saved.get("releaseNotes", ""),
        "checkedAt": saved.get("checkedAt"),
        "phase": saved.get("phase", "idle"),
        "message": saved.get("message", ""),
        "startedAt": saved.get("startedAt"),
        "finishedAt": saved.get("finishedAt"),
        "canRollback": bool(previous and previous != image),
        "previousVersion": image_version(previous),
        "health": container_health(),
        "officialUpdateAllowed": not yeutech_build_required,
        "yeutechBuildRequired": yeutech_build_required,
        "yeutechUpToDate": bool(current_commit and branch_commit and branch_commit.startswith(current_commit)),
        "updateBlockedReason": blocked_reason,
    }


def check_release() -> dict:
    request = urllib.request.Request(RELEASE_API, headers={"Accept": "application/vnd.github+json", "User-Agent": "yeutech-update-agent"})
    with urllib.request.urlopen(request, timeout=20) as response:
        release = json.load(response)
    version = str(release.get("tag_name", ""))
    if not VERSION_RE.fullmatch(version):
        raise RuntimeError("上游稳定版本格式无效")
    notes = str(release.get("body", "")).strip()[:8000]
    branch_commit = yeutech_branch_head()
    included, behind = False, None
    integration_message = '无法确认上游合并状态；未开放安装，请稍后重新检查'
    if branch_commit:
        compare = urllib.parse.quote(version + '...huaye37:' + branch_commit, safe=':')
        request = urllib.request.Request(
            'https://api.github.com/repos/router-for-me/CLIProxyAPI/compare/' + compare,
            headers={'Accept': 'application/vnd.github+json', 'User-Agent': 'yeutech-update-agent'})
        try:
            with urllib.request.urlopen(request, timeout=20) as response:
                comparison = json.load(response)
            included = comparison.get('status') in ('ahead', 'identical')
            behind = comparison.get('behind_by')
            integration_message = (f'已合并官方 {version}；安装时将再次校验并检查空闲发布槽'
                                   if included else f'官方 {version} 尚未合入 YEUTECH 分支（落后 {behind} 个提交）；不会重装旧分支冒充升级')
        except Exception:
            pass
    install_ready = False
    installation_message = ''
    if included:
        try:
            bluegreen.deployment_plan()
            install_ready = True
        except Exception as error:
            installation_message = str(error)
    return write_state(
        upstreamIncluded=included,
        installReady=install_ready,
        installationMessage=installation_message,
        upstreamBehindCommits=behind,
        integrationMessage=integration_message,
        latestVersion=version,
        releaseUrl=str(release.get("html_url", "")),
        releaseNotes=notes,
        checkedAt=int(time.time()),
        branchCommit=branch_commit,
    )


def established_connections() -> int:
    output = subprocess.run(["/usr/bin/netstat", "-tn"], text=True, capture_output=True).stdout
    return sum("ESTABLISHED" in line and ("127.0.0.1:18319" in line or "::1:18319" in line) for line in output.splitlines())


def wait_until_idle(timeout: int = 900) -> None:
    deadline = time.time() + timeout
    quiet_since = None
    while time.time() < deadline:
        if established_connections() == 0:
            quiet_since = quiet_since or time.time()
            if time.time() - quiet_since >= 5:
                return
        else:
            quiet_since = None
        time.sleep(1)
    raise RuntimeError("等待现有模型请求结束超时，代理未更新")


def backup_runtime(version: str, image: str) -> Path:
    BACKUPS.mkdir(mode=0o700, exist_ok=True)
    target = BACKUPS / (time.strftime("%Y%m%d-%H%M%S") + "-" + version)
    target.mkdir(mode=0o700)
    shutil.copy2(COMPOSE, target / "compose.yaml")
    (target / "metadata.json").write_text(json.dumps({"version": version, "image": image, "createdAt": int(time.time())}))
    os.chmod(target / "metadata.json", 0o600)
    active_config = bluegreen.slot_config(bluegreen.production_state()["slot"]) if bluegreen.STATE_FILE.exists() else CONFIG
    with tarfile.open(target / "config-auth.tar.gz", "w:gz") as archive:
        archive.add(active_config, arcname="config", recursive=True)
        archive.add(AUTH, arcname="auth", recursive=True)
    os.chmod(target / "config-auth.tar.gz", 0o600)
    return target


def pinned_image(version: str) -> str:
    tag = f"{IMAGE_REPOSITORY}:{version}"
    run(DOCKER, "pull", tag)
    digests = run(DOCKER, "image", "inspect", tag, "--format", "{{join .RepoDigests \"\\n\"}}", timeout=60).splitlines()
    digest = next((item for item in digests if item.startswith(IMAGE_REPOSITORY + "@sha256:")), "")
    if not digest:
        raise RuntimeError("镜像没有可固定的摘要")
    return f"{tag}@{digest.split('@', 1)[1]}"


def set_compose_image(image: str) -> None:
    source = COMPOSE.read_text()
    if len(IMAGE_RE.findall(source)) != 1:
        raise RuntimeError("代理编排文件中的镜像配置不唯一")
    temporary = COMPOSE.with_suffix(".tmp")
    temporary.write_text(IMAGE_RE.sub(f"    image: {image}", source, count=1))
    temporary.replace(COMPOSE)


def restart_and_verify(image: str) -> None:
    bluegreen.deploy(image)
    if container_health() != "healthy":
        raise RuntimeError("新版代理未通过蓝绿入口健康检查")


def verify_upstream_included(source: Path, version: str) -> None:
    if not VERSION_RE.fullmatch(version):
        raise RuntimeError('上游版本格式无效')
    ref = 'refs/yeutech-upstream/' + version
    run('/usr/bin/git', '-C', str(source), 'fetch', '--no-tags', UPSTREAM_REPOSITORY,
        'refs/tags/' + version + ':' + ref, timeout=180)
    result = subprocess.run(['/usr/bin/git', '-C', str(source), 'merge-base',
                             '--is-ancestor', ref, 'HEAD'], capture_output=True)
    if result.returncode:
        raise RuntimeError(f'YEUTECH 维护分支尚未合并官方 {version}；未构建、未切换代理。请先完成候选合并和验证')
    write_state(upstreamIntegratedVersion=version)


def perform_update(version: str) -> None:
    old_image = current_image()
    old_version = image_version(old_image)
    if is_yeutech_image(old_image):
        return perform_yeutech_update(version)
    if version == old_version:
        write_state(phase="complete", message="当前已经是所选版本", finishedAt=int(time.time()))
        return
    blocked_reason = official_update_block_reason(old_image)
    if blocked_reason:
        raise RuntimeError(blocked_reason)
    write_state(phase="downloading", message="正在下载并校验新版镜像")
    new_image = pinned_image(version)
    backup = backup_runtime(old_version, old_image)
    write_state(phase="waiting", message="正在检查空闲发布槽；现有请求继续运行", previousImage=old_image, backupPath=str(backup))
    write_state(phase="installing", message="正在切换代理版本")
    try:
        restart_and_verify(new_image)
    except Exception as error:
        raise RuntimeError(f"新版切换未完成，已保留原实例和在途请求：{error}") from error
    write_state(phase="complete", message=f"已更新至 {version}", finishedAt=int(time.time()))


def perform_yeutech_update(version: str) -> None:
    """Build the reviewed GitHub branch before touching the live container."""
    old_image = current_image()
    stamp = time.strftime("%Y%m%d-%H%M%S")
    stage = RELEASES / ("web-" + stamp)
    source = stage / "source"
    try:
        write_state(phase="downloading", message="正在获取 GitHub 上已合并的 YEUTECH 兼容版")
        run("/usr/bin/git", "clone", "--single-branch", "--branch", YEUTECH_BRANCH, YEUTECH_REPOSITORY, str(source), timeout=180)
        commit = run("/usr/bin/git", "-C", str(source), "rev-parse", "HEAD", timeout=30)
        verify_upstream_included(source, version)
        bluegreen.deployment_plan()
        image = "cli-proxy-api:yeutech-" + commit[:12]
        write_state(phase="installing", message="正在构建 YEUTECH 兼容镜像", sourceCommit=commit)
        run(DOCKER, "build", "--build-arg", "VERSION=yeutech-" + commit[:12], "--build-arg", "COMMIT=" + commit, "--build-arg", "BUILD_DATE=" + time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "-t", image, str(source), timeout=1800)
        backup = backup_runtime(image_version(old_image), old_image)
        write_state(phase="installing", message="正在切换新请求入口；旧请求继续运行", previousImage=old_image, backupPath=str(backup))
        try:
            restart_and_verify(image)
        except Exception as error:
            raise RuntimeError(f"新版切换未完成，已保留原实例和在途请求：{error}") from error
        write_state(phase="complete", message=f"YEUTECH {version} 已部署（{commit[:12]}）；旧请求在原实例继续完成", finishedAt=int(time.time()), sourceCommit=commit, branchCommit=commit)
    finally:
        shutil.rmtree(stage, ignore_errors=True)


def perform_rollback() -> None:
    saved = read_state()
    previous = str(saved.get("previousImage", ""))
    if not previous or previous == current_image():
        raise RuntimeError("没有可回退的上一版本")
    current = current_image()
    write_state(phase="waiting", message="正在检查回退发布槽；现有请求继续运行", startedAt=int(time.time()))
    write_state(phase="rolling_back", message="正在恢复上一版本")
    try:
        restart_and_verify(previous)
    except Exception as error:
        raise RuntimeError(f"回退未完成，已保留原入口和在途实例：{error}") from error
    write_state(phase="complete", message=f"已回退至 {image_version(previous)}", previousImage=current, finishedAt=int(time.time()))


def start_job(action: str, version: Optional[str] = None) -> None:
    if not job_lock.acquire(blocking=False):
        raise RuntimeError("已有更新任务正在执行")

    def worker() -> None:
        try:
            write_state(phase="starting", message="更新任务已开始", startedAt=int(time.time()), finishedAt=None)
            if action == "update":
                perform_update(version or "")
            else:
                perform_rollback()
        except Exception as error:
            write_state(phase="failed", message=str(error)[:500], finishedAt=int(time.time()))
        finally:
            job_lock.release()

    threading.Thread(target=worker, daemon=True).start()


class Handler(BaseHTTPRequestHandler):
    server_version = "YEUTECHUpdater/1"

    def log_message(self, fmt, *args):
        return

    def send_json(self, code: int, value: dict) -> None:
        data = json.dumps(value, ensure_ascii=False).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(data)

    def authorized(self) -> bool:
        expected = TOKEN_FILE.read_text().strip()
        provided = self.headers.get("Authorization", "")
        provided = provided[7:] if provided.startswith("Bearer ") else provided
        return bool(expected and hmac.compare_digest(expected, provided))

    def do_GET(self):
        if not self.authorized():
            return self.send_json(401, {"error": "unauthorized"})
        if self.path == "/status":
            return self.send_json(200, public_status())
        self.send_json(404, {"error": "not found"})

    def do_POST(self):
        if not self.authorized():
            return self.send_json(401, {"error": "unauthorized"})
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length > 4096:
                raise RuntimeError("请求过大")
            body = json.loads(self.rfile.read(length) or b"{}")
            if self.path == "/check":
                check_release()
                return self.send_json(200, public_status())
            if self.path == "/update":
                version = str(body.get("version", ""))
                saved = read_state()
                if not VERSION_RE.fullmatch(version) or version != saved.get("latestVersion"):
                    raise RuntimeError("只能更新至刚刚检查到的稳定版本")
                if is_yeutech_image(current_image()) and saved.get('upstreamIncluded') is not True:
                    raise RuntimeError(saved.get('integrationMessage') or '请先检查上游合并状态，未开始更新')
                if is_yeutech_image(current_image()) and saved.get('installReady') is not True:
                    raise RuntimeError(saved.get('installationMessage') or '请先检查发布槽和入口配置，未开始更新')
                start_job("update", version)
                return self.send_json(202, public_status())
            if self.path == "/rollback":
                start_job("rollback")
                return self.send_json(202, public_status())
            self.send_json(404, {"error": "not found"})
        except RuntimeError as error:
            self.send_json(409, {"error": str(error)})
        except Exception:
            self.send_json(500, {"error": "更新服务暂时无法完成操作"})


if __name__ == "__main__":
    if not TOKEN_FILE.is_file() or (TOKEN_FILE.stat().st_mode & 0o777) != 0o600:
        raise SystemExit("update-agent.key must exist with mode 0600")
    # Acquire ownership AND bind before changing persisted job state. A failed
    # duplicate process must not mark another controller's live job as failed.
    controller_lock = open(ROOT / 'update-agent.lock', 'a')
    fcntl.flock(controller_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    server = ThreadingHTTPServer((HOST, PORT), Handler)
    if read_state().get("phase") in {"starting", "downloading", "waiting", "installing", "rolling_back"}:
        write_state(phase="failed", message="更新服务重启，已停止上次未完成的任务；请核对当前版本后重试", finishedAt=int(time.time()))
    server.serve_forever()
