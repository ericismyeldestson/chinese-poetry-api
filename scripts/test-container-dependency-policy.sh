#!/bin/sh

set -eu

policy=scripts/verify-container-dependency-policy.sh
dockerfile=Dockerfile
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/container-policy-test.XXXXXX")

cleanup() {
    rm -rf -- "$work_dir"
}
trap cleanup EXIT HUP INT TERM

sh "$policy" "$dockerfile" >/dev/null

sed 's/"git>=2\.52\.0-r0"/git>=2.52.0-r0/' \
    "$dockerfile" >"$work_dir/unquoted"
sed 's/"curl>=8\.14\.1-r3"/"curl>=0"/' \
    "$dockerfile" >"$work_dir/weakened-floor"
sed '/^ARG BUILDKIT_SBOM_SCAN_STAGE=true$/d' \
    "$dockerfile" >"$work_dir/missing-builder-sbom"
awk '
    { print }
    /^ARG BUILDKIT_SBOM_SCAN_STAGE=true$/ {
        print "ARG BUILDKIT_SBOM_SCAN_STAGE=false"
    }
' "$dockerfile" >"$work_dir/overridden-builder-sbom"
sed 's/golang:1\.25\.13-alpine3\.23/golang:1.25.13-alpine3.22/' \
    "$dockerfile" >"$work_dir/wrong-base"
awk '
    /^FROM alpine:3\.22\.5/ {
        print "RUN apk add --no-cache bash"
        print ""
    }
    { print }
' "$dockerfile" >"$work_dir/extra-apk"
awk '
    /^FROM alpine:3\.22\.5/ {
        print "RUN /sbin/apk add --no-cache bash"
        print ""
    }
    { print }
' "$dockerfile" >"$work_dir/path-apk"
awk '
    /^FROM alpine:3\.22\.5/ {
        print "RUN apk \\"
        print "    add --no-cache bash"
        print ""
    }
    { print }
' "$dockerfile" >"$work_dir/split-apk"
awk '
    /^FROM alpine:3\.22\.5/ {
        print "  from alpine:latest AS unnoticed"
        print ""
    }
    { print }
' "$dockerfile" >"$work_dir/lowercase-leading-from"
awk '
    /^FROM alpine:3\.22\.5/ {
        print "RUN [\"apk\", \"add\", \"--no-cache\", \"bash\"]"
        print ""
    }
    { print }
' "$dockerfile" >"$work_dir/json-apk"
awk '
    /^FROM alpine:3\.22\.5/ {
        print "RUN /sbin/apk.static add --no-cache bash"
        print ""
    }
    { print }
' "$dockerfile" >"$work_dir/static-apk"

expect_rejected() {
    fixture=$1
    if sh "$policy" "$work_dir/$fixture" >"$work_dir/$fixture.log" 2>&1; then
        printf 'Container policy unexpectedly accepted fixture: %s\n' "$fixture" >&2
        exit 1
    fi
}

expect_rejected unquoted
expect_rejected weakened-floor
expect_rejected missing-builder-sbom
expect_rejected overridden-builder-sbom
expect_rejected wrong-base
expect_rejected extra-apk
expect_rejected path-apk
expect_rejected split-apk
expect_rejected lowercase-leading-from
expect_rejected json-apk
expect_rejected static-apk

printf '%s\n' 'container dependency policy regression scenarios passed'
