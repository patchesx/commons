#!/bin/sh
set -e

SECRETS_FILE="/data/secrets.env"

mkdir -p /data
touch "$SECRETS_FILE"
chmod 0600 "$SECRETS_FILE"

# shellcheck source=/dev/null
. "$SECRETS_FILE"

# For each secret: keep existing value (from file or env), generate only if absent.
_ensure_secret() {
  local key="$1"
  local current="$2"
  if [ -z "$current" ]; then
    local generated
    generated="$(openssl rand -hex 32)"
    printf '%s=%s\n' "$key" "$generated" >> "$SECRETS_FILE"
    echo "install: generated $key"
    # Export the newly generated value into the current shell.
    export "$key=$generated"
  fi
}

_ensure_secret SESSION_SECRET  "$SESSION_SECRET"
_ensure_secret ENCRYPTION_KEY  "$ENCRYPTION_KEY"

# Re-source so exported vars reflect any values appended above.
# shellcheck source=/dev/null
. "$SECRETS_FILE"
export SESSION_SECRET
export ENCRYPTION_KEY

exec /app/server "$@"
