#!/bin/sh

set -eu

dockerfile=${1:-Dockerfile}

if [ ! -f "$dockerfile" ]; then
    printf 'Container dependency contract cannot find %s\n' "$dockerfile" >&2
    exit 1
fi

# A digest freezes each base root filesystem. Package installation still reads
# the selected stable branch's signed APKINDEX, so direct dependencies use
# reviewed lower bounds instead of revisions that disappear when Alpine ships a
# security or maintenance update.
if ! awk '
    function valid_reference(reference, expected_image, separator, image, digest, component) {
        separator = index(reference, "@sha256:")
        if (separator == 0) {
            return 0
        }
        image = substr(reference, 1, separator - 1)
        digest = substr(reference, separator + 8)
        component = image
        sub(/^.*\//, "", component)
        return image == expected_image && index(component, ":") > 0 &&
            length(digest) == 64 && digest ~ /^[0-9a-f]+$/
    }
    {
        line = $0
        sub(/^[[:space:]]+/, "", line)
        lower = tolower(line)
    }
    lower ~ /^from([[:space:]]|$)/ {
        fields = split(line, part, /[[:space:]]+/)
        total++
        if (total == 1) {
            if (fields != 4 || toupper(part[1]) != "FROM" ||
                toupper(part[3]) != "AS" || part[4] != "builder" ||
                !valid_reference(part[2], "golang:1.25.13-alpine3.23")) {
                print "Builder base image contract failed"
                invalid = 1
            }
        } else if (total == 2) {
            if (fields != 2 || toupper(part[1]) != "FROM" ||
                !valid_reference(part[2], "alpine:3.22.5")) {
                print "Runtime base image contract failed"
                invalid = 1
            }
        } else {
            print "Unexpected additional base image"
            invalid = 1
        }
    }
    END {
        if (total != 2) {
            print "Expected exactly two base images"
            invalid = 1
        }
        if (invalid) {
            exit 1
        }
    }
' "$dockerfile" >&2; then
    printf '%s\n' 'Every Dockerfile base image must match its reviewed tag and sha256 policy' >&2
    exit 1
fi

if ! awk '
    {
        line = $0
        sub(/^[[:space:]]+/, "", line)
        lower = tolower(line)
    }
    lower ~ /^from([[:space:]]|$)/ {
        stage++
    }
    lower ~ /^arg[[:space:]]+buildkit_sbom_scan_stage([=[:space:]]|$)/ {
        builder_scan_args++
    }
    stage == 1 && line == "ARG BUILDKIT_SBOM_SCAN_STAGE=true" {
        builder_scan++
    }
    END {
        exit builder_scan_args == 1 && builder_scan == 1 ? 0 : 1
    }
' "$dockerfile"; then
    printf '%s\n' 'The builder stage must be included in the release SBOM' >&2
    exit 1
fi

if ! awk '
    BEGIN {
        expected["builder" SUBSEP "git"] = "git>=2.52.0-r0"
        expected["builder" SUBSEP "gcc"] = "gcc>=15.2.0-r2"
        expected["builder" SUBSEP "musl-dev"] = "musl-dev>=1.2.5-r23"
        expected["builder" SUBSEP "sqlite-dev"] = "sqlite-dev>=3.51.2-r0"
        expected["runtime" SUBSEP "ca-certificates"] = "ca-certificates>=20260611-r0"
        expected["runtime" SUBSEP "curl"] = "curl>=8.14.1-r3"
        expected["runtime" SUBSEP "gzip"] = "gzip>=1.14-r1"
        expected["runtime" SUBSEP "sqlite"] = "sqlite>=3.49.2-r1"
        expected["runtime" SUBSEP "tzdata"] = "tzdata>=2026c-r0"
    }
    function reject(message) {
        print message
        invalid = 1
    }
    {
        line = $0
        sub(/^[[:space:]]+/, "", line)
        lower = tolower(line)
    }
    lower ~ /^from([[:space:]]|$)/ {
        current_stage = lower ~ /[[:space:]]as[[:space:]]+builder$/ ? "builder" : "runtime"
    }
    $0 !~ /^[[:space:]]*#/ &&
        lower ~ /(^|[^[:alnum:]_])apk([^[:alnum:]_]|$)/ {
        apk_calls++
        if ($0 != "RUN apk add --no-cache \\") {
            reject("Every apk command must be one of the reviewed dependency blocks")
            next
        }
        in_apk = 1
        block_stage = current_stage
        blocks[block_stage]++
        next
    }
    in_apk {
        raw = $0
        if (raw ~ /^[[:space:]]*&&/) {
            in_apk = 0
            next
        }
        if (raw !~ /^[[:space:]]+/) {
            reject("APK dependency block ended unexpectedly")
            in_apk = 0
            next
        }
        continued = raw ~ /\\[[:space:]]*$/
        line = raw
        sub(/^[[:space:]]+/, "", line)
        sub(/[[:space:]]*\\[[:space:]]*$/, "", line)
        if (substr(line, 1, 1) != "\"" ||
            substr(line, length(line), 1) != "\"") {
            reject("APK dependency must be a quoted lower-bound constraint: " line)
        } else {
            line = substr(line, 2, length(line) - 2)
            separator = index(line, ">=")
            if (separator <= 1) {
                reject("APK dependency must use >=: " line)
            } else {
                name = substr(line, 1, separator - 1)
                key = block_stage SUBSEP name
                if (!(key in expected)) {
                    reject("Unexpected direct APK dependency in " block_stage ": " name)
                } else if (line != expected[key]) {
                    reject("APK dependency baseline differs from policy: " line)
                }
                seen[key]++
            }
        }
        if (!continued) {
            in_apk = 0
        }
    }
    END {
        if (in_apk) {
            reject("APK dependency block is unterminated")
        }
        if (apk_calls != 2 || blocks["builder"] != 1 || blocks["runtime"] != 1) {
            reject("Expected one apk dependency block in each image stage")
        }
        for (key in expected) {
            if (seen[key] != 1) {
                split(key, parts, SUBSEP)
                reject("Missing or duplicate APK dependency: " parts[1] "/" parts[2])
            }
        }
        if (invalid) {
            exit 1
        }
    }
' "$dockerfile" >&2; then
    printf '%s\n' 'Container APK dependency policy failed' >&2
    exit 1
fi

printf '%s\n' 'container dependency policy verified'
