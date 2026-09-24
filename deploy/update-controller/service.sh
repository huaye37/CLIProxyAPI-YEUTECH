#!/bin/sh
set -eu
PIDFILE=/var/run/yeutech-proxy-updater.pid
SCRIPT=/volume1/docker/yeutech-api-manager/updater/agent.py
LOG=/volume1/docker/yeutech-api-manager/data/updater.log

running_pid() {
  ps -eo pid,args | awk '$2 == "/usr/bin/python3" && $3 == "/volume1/docker/yeutech-api-manager/updater/agent.py" && NF == 3 {print $1}'
}

start() {
  current=$(running_pid)
  if [ -n "$current" ]; then
    test "$(printf '%s\n' "$current" | wc -l | tr -d ' ')" = 1
    printf '%s\n' "$current" > "$PIDFILE"
    return
  fi
  /usr/bin/python3 "$SCRIPT" >> "$LOG" 2>&1 &
  candidate=$!
  sleep 1
  kill -0 "$candidate"
  printf '%s\n' "$candidate" > "$PIDFILE"
}

stop() {
  current=$(running_pid)
  if [ -n "$current" ]; then
    test "$(printf '%s\n' "$current" | wc -l | tr -d ' ')" = 1
    kill "$current"
    count=0
    while kill -0 "$current" 2>/dev/null; do
      count=$((count + 1))
      if [ "$count" -ge 10 ]; then
        echo 'Controller did not exit; refusing to start a duplicate' >&2
        return 1
      fi
      sleep 1
    done
  fi
  if [ -f "$PIDFILE" ]; then unlink "$PIDFILE"; fi
}

case "${1:-status}" in
  start) start ;;
  stop) stop ;;
  restart) stop; start ;;
  status) test -n "$(running_pid)" ;;
  *) exit 2 ;;
esac
