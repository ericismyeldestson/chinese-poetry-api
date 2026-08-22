#!/usr/bin/env bash

set -Eeuo pipefail

die() {
    echo "ERROR: $*" >&2
    exit 1
}

[[ $# -ge 4 && $# -le 5 ]] || die \
    "usage: $0 IMAGE@DIGEST REVISION VERSION DATA_TAG [EVIDENCE_DIR]"

image_ref=$1
expected_revision=$2
expected_version=$3
expected_data_tag=$4
evidence_dir=${5:-}

[[ $image_ref =~ ^ghcr\.io/ericismyeldestson/chinese-poetry-api@sha256:[0-9a-f]{64}$ ]] ||
    die "image reference must pin the official GHCR image by sha256 digest"
[[ $expected_revision =~ ^[0-9a-f]{40}$ ]] || die "invalid expected revision"
[[ $expected_version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "invalid image version"
[[ $expected_data_tag =~ ^v[0-9]+\.[0-9]+\.0$ ]] || die "invalid data release tag"
[[ -n ${GITHUB_REPOSITORY:-} && -n ${GITHUB_RUN_ID:-} &&
    ${GITHUB_RUN_ATTEMPT:-} =~ ^[1-9][0-9]*$ ]] ||
    die "GitHub workflow identity is required"
[[ $GITHUB_REPOSITORY == ericismyeldestson/chinese-poetry-api ]] ||
    die "unexpected GitHub repository identity"

for command in docker jq mktemp cp; do
    command -v "$command" >/dev/null 2>&1 || die "missing command: $command"
done

expected_digest=${image_ref##*@}
work_dir=$(mktemp -d)
cleanup() {
    rm -rf -- "$work_dir"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

inspect_json() {
    local template=$1
    local output=$2
    local attempt

    for attempt in 1 2 3 4 5 6; do
        if docker buildx imagetools inspect "$image_ref" \
            --format "$template" >"$work_dir/${output}.tmp" \
            2>"$work_dir/${output}.error"; then
            mv "$work_dir/${output}.tmp" "$work_dir/$output"
            return 0
        fi
        if [[ $attempt == 6 ]]; then
            cat "$work_dir/${output}.error" >&2
            die "could not inspect $output after six attempts"
        fi
        sleep 5
    done
}

inspect_json '{{json .Manifest}}' manifest.json
inspect_json '{{json .Image}}' image.json
inspect_json '{{json .SBOM}}' sbom.json
inspect_json '{{json .Provenance}}' provenance.json

jq -e --arg digest "$expected_digest" '
    .digest == $digest and
    .mediaType == "application/vnd.oci.image.index.v1+json" and
    (.manifests | type) == "array" and
    (.manifests | length) == 4 and
    all(
        .manifests[];
        .mediaType == "application/vnd.oci.image.manifest.v1+json" and
        (.digest | type) == "string" and
        (.digest | test("^sha256:[0-9a-f]{64}$")) and
        (.size | type) == "number" and .size > 0
    ) and
    ([
        .manifests[] |
        select(.platform.os == "linux") |
        (.platform.os + "/" + .platform.architecture)
    ] | sort) == ["linux/amd64", "linux/arm64"] and
    ([
        .manifests[] |
        select(
            .platform.os == "unknown" and
            .platform.architecture == "unknown" and
            .annotations["vnd.docker.reference.type"] ==
                "attestation-manifest"
        )
    ] | length) == 2 and
    ([
        .manifests[] |
        select(.platform.os == "linux") |
        .digest
    ] | sort) == ([
        .manifests[] |
        select(.platform.os == "unknown") |
        .annotations["vnd.docker.reference.digest"]
    ] | sort)
' "$work_dir/manifest.json" >/dev/null || die "published OCI index contract failed"

jq -e \
    --arg revision "$expected_revision" \
    --arg version "$expected_version" \
    --arg data_tag "$expected_data_tag" '
    (keys | sort) == ["linux/amd64", "linux/arm64"] and
    all(
        to_entries[];
        .key as $platform |
        .value.os == "linux" and
        .value.architecture == ($platform | split("/")[1]) and
        .value.rootfs.type == "layers" and
        (.value.rootfs.diff_ids | type) == "array" and
        (.value.rootfs.diff_ids | length) > 0 and
        all(
            .value.rootfs.diff_ids[];
            test("^sha256:[0-9a-f]{64}$")
        ) and
        .value.config.User == "10001:10001" and
        .value.config.Entrypoint == ["./startup.sh"] and
        .value.config.Labels["org.opencontainers.image.source"] ==
            "https://github.com/ericismyeldestson/chinese-poetry-api" and
        .value.config.Labels["org.opencontainers.image.revision"] == $revision and
        .value.config.Labels["org.opencontainers.image.version"] == $version and
        .value.config.Labels["org.opencontainers.image.licenses"] ==
            "GPL-3.0-only" and
        (.value.config.Env | type) == "array" and
        any(
            .value.config.Env[];
            . == ("DATA_RELEASE_VERSION=" + $data_tag)
        )
    )
' "$work_dir/image.json" >/dev/null || die "published runtime config contract failed"

jq -e '
    def documents: [.SPDX] + .AdditionalSPDXs;
    def valid_spdx:
        .SPDXID == "SPDXRef-DOCUMENT" and
        .spdxVersion == "SPDX-2.3" and
        .dataLicense == "CC0-1.0" and
        (.packages | type) == "array" and
        (.packages | length) > 0 and
        (.files | type) == "array" and
        (.files | length) > 0;
    def has_package($name):
        any(.packages[];
            .name == $name and
            (.versionInfo | type) == "string" and
            (.versionInfo | length) > 0);
    def is_builder:
        has_package("git") and has_package("gcc") and
        has_package("musl-dev") and has_package("sqlite-dev");
    def is_runtime:
        any(.packages[];
            .name == "github.com/ericismyeldestson/chinese-poetry-api") and
        has_package("ca-certificates") and has_package("curl") and
        has_package("gzip") and has_package("sqlite") and has_package("tzdata");
    def package_version($name):
        [.packages[] |
            select(.name == $name) |
            .versionInfo |
            select(type == "string" and length > 0)] |
        unique |
        if length == 1 then .[0] else null end;

    . as $root |
    (keys | sort) == ["linux/amd64", "linux/arm64"] and
    all(
        to_entries[];
        .value as $record |
        (($record | keys | sort) == ["AdditionalSPDXs", "SPDX"]) and
        (($record.AdditionalSPDXs | type) == "array") and
        (($record.AdditionalSPDXs | length) == 1) and
        (($record | documents) as $documents |
            ($documents | length) == 2 and
            all($documents[]; valid_spdx) and
            all($documents[];
                (is_builder or is_runtime) and
                ((is_builder and is_runtime) | not)) and
            ([$documents[] | select(is_builder)] | length) == 1 and
            ([$documents[] | select(is_runtime)] | length) == 1)
    ) and
    all(
        ["git", "gcc", "musl-dev", "sqlite-dev"][];
        . as $name |
        ([$root | to_entries[] |
            (.value | documents)[] |
            select(is_builder) |
            package_version($name)]) as $versions |
        ($versions | length) == 2 and
        all($versions[]; . != null) and
        ($versions | unique | length) == 1
    ) and
    all(
        ["ca-certificates", "curl", "gzip", "sqlite", "tzdata"][];
        . as $name |
        ([$root | to_entries[] |
            (.value | documents)[] |
            select(is_runtime) |
            package_version($name)]) as $versions |
        ($versions | length) == 2 and
        all($versions[]; . != null) and
        ($versions | unique | length) == 1
    )
' "$work_dir/sbom.json" >/dev/null ||
    die "published runtime and builder SPDX SBOM contract failed"

builder_prefix="https://github.com/${GITHUB_REPOSITORY}/actions/runs/${GITHUB_RUN_ID}/attempts/"
jq -e \
    --arg revision "$expected_revision" \
    --arg builder_prefix "$builder_prefix" \
    --argjson current_attempt "$GITHUB_RUN_ATTEMPT" '
    (keys | sort) == ["linux/amd64", "linux/arm64"] and
    ([to_entries[].value.SLSA.runDetails.builder.id] | unique | length) == 1 and
    all(
        to_entries[];
        .key as $platform |
        (.value | keys) == ["SLSA"] and
        .value.SLSA.buildDefinition.buildType ==
            "https://github.com/moby/buildkit/blob/master/docs/attestations/slsa-definitions.md" and
        .value.SLSA.buildDefinition.externalParameters.request.args[
            "build-arg:VCS_REF"
        ] == $revision and
        .value.SLSA.buildDefinition.externalParameters.request.root.request.args[
            "vcs:revision"
        ] == $revision and
        (
            .value.SLSA.buildDefinition.externalParameters.request.root.request.args[
                "vcs:source"
            ] == "https://github.com/ericismyeldestson/chinese-poetry-api" or
            .value.SLSA.buildDefinition.externalParameters.request.root.request.args[
                "vcs:source"
            ] == "https://github.com/ericismyeldestson/chinese-poetry-api.git"
        ) and
        (.value.SLSA.buildDefinition.resolvedDependencies | type) == "array" and
        (.value.SLSA.buildDefinition.resolvedDependencies | length) > 0 and
        all(
            .value.SLSA.buildDefinition.resolvedDependencies[];
            (.digest.sha256 | type) == "string" and
            (.digest.sha256 | test("^[0-9a-f]{64}$"))
        ) and
        any(
            .value.SLSA.buildDefinition.resolvedDependencies[];
            .uri | contains("platform=" + ($platform | @uri))
        ) and
        (.value.SLSA.runDetails.builder.id | type) == "string" and
        (.value.SLSA.runDetails.builder.id | startswith($builder_prefix)) and
        (
            .value.SLSA.runDetails.builder.id |
            ltrimstr($builder_prefix) |
            test("^[1-9][0-9]*$")
        ) and
        (
            .value.SLSA.runDetails.builder.id |
            ltrimstr($builder_prefix) |
            tonumber
        ) <= $current_attempt
    )
' "$work_dir/provenance.json" >/dev/null || die "published SLSA provenance contract failed"

if [[ -n $evidence_dir ]]; then
    mkdir -p "$evidence_dir"
    cp "$work_dir/manifest.json" "$evidence_dir/manifest.json"
    cp "$work_dir/image.json" "$evidence_dir/image.json"
    cp "$work_dir/sbom.json" "$evidence_dir/sbom.json"
    cp "$work_dir/provenance.json" "$evidence_dir/provenance.json"
fi

echo "published multi-platform image, runtime metadata, runtime and builder SPDX SBOMs, and SLSA provenance verified"
