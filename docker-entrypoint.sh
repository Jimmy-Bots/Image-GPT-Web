#!/bin/sh
set -eu

APP_USER="${APP_USER:-appuser}"
APP_GROUP="${APP_GROUP:-appuser}"
DATA_DIR="${CHATGPT2API_DATA_DIR:-/app/data}"
DB_PATH="${CHATGPT2API_DB_PATH:-$DATA_DIR/app.db}"
DB_DIR="$(dirname "$DB_PATH")"
IMAGES_DIR="${CHATGPT2API_IMAGES_DIR:-$DATA_DIR/images}"
BACKUPS_DIR="${CHATGPT2API_BACKUPS_DIR:-$DATA_DIR/backups}"

mkdir -p "$DATA_DIR" "$DB_DIR" "$IMAGES_DIR" "$BACKUPS_DIR"

if [ "$(id -u)" = "0" ]; then
  chown "$APP_USER:$APP_GROUP" "$DATA_DIR" "$DB_DIR" "$IMAGES_DIR" "$BACKUPS_DIR"
  for file in "$DB_PATH" "$DB_PATH-wal" "$DB_PATH-shm"; do
    if [ -e "$file" ]; then
      chown "$APP_USER:$APP_GROUP" "$file"
    fi
  done
  exec gosu "$APP_USER" "$@"
fi

exec "$@"
