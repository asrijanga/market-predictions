#!/bin/sh
# A mounted volume arrives owned by root and shadows whatever the image
# prepared at that path, so the unprivileged user the image switches to
# cannot write to it. Fix the ownership here, where we are still root, then
# drop privileges for the actual process.
set -e

: "${DATA_DIR:=/data}"

if [ "$(id -u)" = "0" ]; then
	mkdir -p "$DATA_DIR"
	chown -R mkt:mkt "$DATA_DIR"
	# exec so the server keeps PID 1 and receives Fly's signals directly.
	exec setpriv --reuid=mkt --regid=mkt --init-groups "$@"
fi

exec "$@"
