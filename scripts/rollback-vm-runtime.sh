#!/bin/sh
set -eu
if [ "$#" -ne 1 ]; then
  echo "usage: rollback-vm-runtime.sh PREFIX" >&2
  exit 2
fi
prefix=$1
if [ ! -L "$prefix/current" ] || [ ! -f "$prefix/previous" ]; then
  echo "no previous vm runtime to roll back to" >&2
  exit 1
fi
current=$(readlink "$prefix/current")
previous=$(cat "$prefix/previous")
if [ ! -d "$previous" ] || [ -L "$previous" ]; then
  echo "previous vm runtime is missing: $previous" >&2
  exit 1
fi
ln -sfn "$previous" "$prefix/current"
printf '%s\n' "$current" > "$prefix/previous"
echo "rolled back to $previous"
