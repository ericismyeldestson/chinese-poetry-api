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
sed 's/"curl>=8\.21\.0-r0"/"curl>=0"/' \
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
sed 's/4ce6af6747b07e99ca3a57eadb77565787390a41b0039dcc8e09ec4c57cfa125/14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce/' \
    "$dockerfile" >"$work_dir/wrong-builder-digest"
sed 's/28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b/14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce/' \
    "$dockerfile" >"$work_dir/wrong-runtime-digest"
awk '
    /^FROM alpine:3\.24\.1/ {
        print "RUN apk add --no-cache bash"
        print ""
    }
    { print }
' "$dockerfile" >"$work_dir/extra-apk"
awk '
    /^FROM alpine:3\.24\.1/ {
        print "RUN /sbin/apk add --no-cache bash"
        print ""
    }
    { print }
' "$dockerfile" >"$work_dir/path-apk"
awk '
    /^FROM alpine:3\.24\.1/ {
        print "RUN apk \\"
        print "    add --no-cache bash"
        print ""
    }
    { print }
' "$dockerfile" >"$work_dir/split-apk"
awk '
    /^FROM alpine:3\.24\.1/ {
        print "  from alpine:latest AS unnoticed"
        print ""
    }
    { print }
' "$dockerfile" >"$work_dir/lowercase-leading-from"
awk '
    /^FROM alpine:3\.24\.1/ {
        print "RUN [\"apk\", \"add\", \"--no-cache\", \"bash\"]"
        print ""
    }
    { print }
' "$dockerfile" >"$work_dir/json-apk"
awk '
    /^FROM alpine:3\.24\.1/ {
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
expect_rejected wrong-builder-digest
expect_rejected wrong-runtime-digest
expect_rejected extra-apk
expect_rejected path-apk
expect_rejected split-apk
expect_rejected lowercase-leading-from
expect_rejected json-apk
expect_rejected static-apk

printf '%s\n' 'container dependency policy regression scenarios passed'
