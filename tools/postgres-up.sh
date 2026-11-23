#!/usr/bin/env bash
# Start a PostgreSQL container for integration tests and wait until it
# accepts connections over TCP; make parse-postgres-up and
# cookbook-postgres-up run it.
#
#   tools/postgres-up.sh --name N --image I --port P --user U --ready-seconds S [--platform OS/ARCH]
#
# The server listens on 127.0.0.1:P, with U as its user, password and
# database. Nothing is written to stdout, which the make targets leave to the
# exports they print. When the container stops while it starts, or is not
# ready S seconds after it started, its log is printed and the container
# removed, so a rerun starts cleanly; so is an interrupted run's. Only a
# container this run created is ever removed: when docker refuses to create
# one, such as for a name already in use, the existing container is left as
# it is.
set -euo pipefail

usage() {
	echo "usage: $0 --name N --image I --port P --user U --ready-seconds S [--platform OS/ARCH]" >&2
	exit 2
}

name='' image='' port='' user='' ready_seconds='' platform=''
while [ $# -gt 0 ]; do
	[ $# -ge 2 ] || usage
	case $1 in
	--name) name=$2 ;;
	--image) image=$2 ;;
	--port) port=$2 ;;
	--user) user=$2 ;;
	--ready-seconds) ready_seconds=$2 ;;
	--platform) platform=$2 ;;
	*) usage ;;
	esac
	shift 2
done
[ -n "$name" ] && [ -n "$image" ] && [ -n "$port" ] && [ -n "$user" ] || usage
case $ready_seconds in
'' | *[!0-9]* | 0) echo "$name: --ready-seconds must be a positive integer, got '$ready_seconds'" >&2 && exit 2 ;;
esac

# docker writes the cidfile once it has created the container, so it names
# the container to remove even when docker run fails after creating it (a
# port already bound), and names none when docker created nothing.
scratch=$(mktemp -d)
cid=$scratch/cid
trap 'rm -rf "$scratch"' EXIT

# remove prints the log of the container this run created, if it created
# one, and removes it.
remove() {
	[ -s "$cid" ] || return 0
	local id
	id=$(<"$cid")
	rm -f "$cid"
	echo "--- $name log" >&2
	docker logs "$id" >&2 || true
	echo "--- end of $name log" >&2
	docker rm -f "$id" >/dev/null || true
}
trap 'echo "$name: interrupted" >&2; remove; exit 130' INT TERM HUP

run=(docker run -d --cidfile "$cid" --name "$name"
	-e "POSTGRES_USER=$user" -e "POSTGRES_PASSWORD=$user" -e "POSTGRES_DB=$user"
	-p "127.0.0.1:$port:5432")
[ -z "$platform" ] || run+=(--platform "$platform")
run+=("$image")
if ! "${run[@]}" >/dev/null; then
	echo "$name: docker run failed" >&2
	remove
	exit 1
fi
id=$(<"$cid")
# From the start, not before the image was pulled.
deadline=$((SECONDS + ready_seconds))

# The image's entrypoint initializes the database on a server that listens
# only on its socket, so a TCP check passes only on the server it then starts.
while :; do
	status=$(docker inspect -f '{{.State.Status}} (exit code {{.State.ExitCode}})' "$id" 2>/dev/null) || status='gone'
	case $status in
	running\ *) ;;
	*)
		echo "$name: stopped while starting: $status" >&2
		remove
		exit 1
		;;
	esac
	if docker exec "$id" pg_isready -q -t 1 -h 127.0.0.1 -U "$user" -d "$user"; then
		exit 0
	fi
	if [ "$SECONDS" -ge "$deadline" ]; then
		echo "$name: not ready after ${ready_seconds}s" >&2
		remove
		exit 1
	fi
	sleep 1
done
