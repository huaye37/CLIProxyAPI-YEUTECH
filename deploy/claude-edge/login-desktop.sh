#!/bin/sh
set -eu
umask 077
profile_dir=/var/lib/yeutech-claude-login/profile
mkdir -p "$profile_dir"
fluxbox >/dev/null 2>&1 &
wm_pid=$!
x11vnc -display "$DISPLAY" -auth "$XAUTHORITY" -no6 -noipv6 \
  -listen 127.0.0.1 -rfbport 5901 -rfbportv6 -1 \
  -nopw -forever -shared -noxdamage -quiet >/dev/null 2>&1 &
vnc_pid=$!
websockify --web /usr/share/novnc 127.0.0.1:6080 127.0.0.1:5901 >/dev/null 2>&1 &
web_pid=$!
trap 'kill "$wm_pid" "$vnc_pid" "$web_pid" 2>/dev/null || true' EXIT HUP INT TERM
dbus-run-session -- chromium --user-data-dir="$profile_dir" --no-first-run \
  --no-default-browser-check --disable-dev-shm-usage --start-maximized \
  https://claude.ai/login

