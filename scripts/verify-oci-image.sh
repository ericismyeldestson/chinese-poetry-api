#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
    echo "usage: $0 OCI_ARCHIVE EXPECTED_REVISION [EVIDENCE_DIR]" >&2
    exit 2
}

die() {
    echo "ERROR: $*" >&2
    exit 1
}

[[ $# -ge 2 && $# -le 3 ]] || usage
archive=$1
expected_revision=$2
evidence_dir=${3:-}

[[ -s $archive ]] || die "OCI archive is missing or empty: $archive"
[[ $expected_revision =~ ^[0-9a-f]{40}$ ]] || die "expected revision must be a full Git SHA"

for command in awk find gzip jq sha256sum tar tr wc; do
    command -v "$command" >/dev/null 2>&1 || die "$command is required"
done

oci=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/chinese-poetry-oci.XXXXXX")
trap 'rm -rf -- "$oci"' EXIT HUP INT TERM
tar -xf "$archive" -C "$oci"

[[ $(jq -er '.imageLayoutVersion' "$oci/oci-layout") == "1.0.0" ]] ||
    die "unsupported or missing OCI layout version"
[[ -s $oci/index.json ]] || die "OCI layout index is missing"
[[ -d $oci/blobs/sha256 ]] || die "OCI blob directory is missing"

blob_path() {
    local digest=$1
    [[ $digest =~ ^sha256:[0-9a-f]{64}$ ]] || die "invalid OCI digest: $digest"
    printf '%s/blobs/sha256/%s\n' "$oci" "${digest#sha256:}"
}

verify_descriptor() {
    local digest=$1
    local expected_size=$2
    local path actual_size actual_digest
    path=$(blob_path "$digest")
    [[ -f $path ]] || die "referenced OCI blob is missing: $digest"
    actual_size=$(wc -c <"$path" | tr -d '[:space:]')
    [[ $actual_size == "$expected_size" ]] ||
        die "OCI blob size mismatch for $digest: expected $expected_size, got $actual_size"
    actual_digest=$(sha256sum "$path" | awk '{ print $1 }')
    [[ sha256:$actual_digest == "$digest" ]] || die "OCI blob digest mismatch: $digest"
}

while IFS= read -r -d '' path; do
    expected=${path##*/}
    [[ $expected =~ ^[0-9a-f]{64}$ ]] || die "invalid OCI blob filename: $path"
    actual=$(sha256sum "$path" | awk '{ print $1 }')
    [[ $actual == "$expected" ]] || die "content-addressed OCI blob mismatch: $path"
done < <(find "$oci/blobs/sha256" -type f -print0)

root_index=$oci/index.json
if jq -e '
    .schemaVersion == 2 and
    .mediaType == "application/vnd.oci.image.index.v1+json" and
    (.manifests | length) == 1 and
    .manifests[0].mediaType == "application/vnd.oci.image.index.v1+json"
' "$root_index" >/dev/null; then
    wrapper_digest=$(jq -er '.manifests[0].digest' "$root_index")
    wrapper_size=$(jq -er '.manifests[0].size' "$root_index")
    verify_descriptor "$wrapper_digest" "$wrapper_size"
    root_index=$(blob_path "$wrapper_digest")
fi

jq -e '
    .schemaVersion == 2 and
    .mediaType == "application/vnd.oci.image.index.v1+json" and
    (.manifests | type) == "array" and
    (.manifests | length) == 4 and
    ([.manifests[] |
        select(.platform.os == "linux") |
        (.platform.os + "/" + .platform.architecture)] | sort) ==
        ["linux/amd64", "linux/arm64"] and
    ([.manifests[] |
        select(.platform.os == "unknown" and .platform.architecture == "unknown" and
            .annotations["vnd.docker.reference.type"] == "attestation-manifest")] | length) == 2
' "$root_index" >/dev/null || die "unexpected multi-platform OCI index shape"

jq -e '
    all(.manifests[];
        .mediaType == "application/vnd.oci.image.manifest.v1+json" and
        (.digest | type) == "string" and
        (.size | type) == "number" and
        .size > 0)
' "$root_index" >/dev/null || die "invalid multi-platform OCI descriptors"

if [[ -n $evidence_dir ]]; then
    mkdir -p "$evidence_dir"
    cp "$root_index" "$evidence_dir/root-index.json"
fi

for arch in amd64 arm64; do
    image_descriptors=()
    while IFS= read -r descriptor; do
        image_descriptors+=("$descriptor")
    done < <(jq -c --arg arch "$arch" '
        .manifests[] | select(.platform.os == "linux" and .platform.architecture == $arch)
    ' "$root_index")
    ((${#image_descriptors[@]} == 1)) || die "linux/$arch must have exactly one image manifest"
    image_descriptor=${image_descriptors[0]}
    image_digest=$(jq -er '.digest' <<<"$image_descriptor")
    image_size=$(jq -er '.size' <<<"$image_descriptor")
    verify_descriptor "$image_digest" "$image_size"
    image_manifest=$(blob_path "$image_digest")
    jq -e '
        .schemaVersion == 2 and
        .mediaType == "application/vnd.oci.image.manifest.v1+json" and
        (.layers | type) == "array" and
        (.layers | length) > 0 and
        all(.layers[];
            .mediaType == "application/vnd.oci.image.layer.v1.tar+gzip" and
            (.digest | type) == "string" and
            (.size | type) == "number" and
            .size > 0)
    ' "$image_manifest" >/dev/null || die "invalid linux/$arch image manifest"

    jq -e '
        .config.mediaType == "application/vnd.oci.image.config.v1+json" and
        (.config.digest | type) == "string" and
        (.config.size | type) == "number" and
        .config.size > 0
    ' "$image_manifest" >/dev/null || die "invalid linux/$arch image config descriptor"
    config_digest=$(jq -er '.config.digest' "$image_manifest")
    config_size=$(jq -er '.config.size' "$image_manifest")
    verify_descriptor "$config_digest" "$config_size"
    config=$(blob_path "$config_digest")
    layer_count=$(jq -er '.layers | length' "$image_manifest")
    jq -e --arg arch "$arch" --arg revision "$expected_revision" \
        --argjson layer_count "$layer_count" '
        .os == "linux" and
        .architecture == $arch and
        .rootfs.type == "layers" and
        (.rootfs.diff_ids | type) == "array" and
        (.rootfs.diff_ids | length) == $layer_count and
        all(.rootfs.diff_ids[]; test("^sha256:[0-9a-f]{64}$")) and
        .config.User == "10001:10001" and
        .config.Entrypoint == ["./startup.sh"] and
        .config.Labels["org.opencontainers.image.revision"] == $revision and
        .config.Labels["org.opencontainers.image.source"] ==
            "https://github.com/ericismyeldestson/chinese-poetry-api" and
        .config.Labels["org.opencontainers.image.licenses"] == "GPL-3.0-only"
    ' "$config" >/dev/null || die "linux/$arch runtime config contract failed"

    layer_index=0
    while IFS=$'\t' read -r layer_digest layer_size; do
        verify_descriptor "$layer_digest" "$layer_size"
        layer=$(blob_path "$layer_digest")
        expected_diff_id=$(jq -er --argjson index "$layer_index" \
            '.rootfs.diff_ids[$index]' "$config")
        actual_diff_id=sha256:$(gzip -dc "$layer" | sha256sum | awk '{ print $1 }')
        [[ $actual_diff_id == "$expected_diff_id" ]] ||
            die "linux/$arch layer $layer_index does not match its runtime diff ID"
        layer_index=$((layer_index + 1))
    done < <(jq -r '.layers[] | [.digest, (.size | tostring)] | @tsv' "$image_manifest")

    attestation_descriptors=()
    while IFS= read -r descriptor; do
        attestation_descriptors+=("$descriptor")
    done < <(jq -c --arg image "$image_digest" '
        .manifests[] |
        select(.annotations["vnd.docker.reference.type"] == "attestation-manifest" and
            .annotations["vnd.docker.reference.digest"] == $image)
    ' "$root_index")
    ((${#attestation_descriptors[@]} == 1)) ||
        die "linux/$arch must have exactly one linked attestation manifest"
    attestation_descriptor=${attestation_descriptors[0]}
    attestation_digest=$(jq -er '.digest' <<<"$attestation_descriptor")
    attestation_size=$(jq -er '.size' <<<"$attestation_descriptor")
    verify_descriptor "$attestation_digest" "$attestation_size"
    attestation_manifest=$(blob_path "$attestation_digest")

    jq -e '
        .schemaVersion == 2 and
        .mediaType == "application/vnd.oci.image.manifest.v1+json" and
        (.layers | type) == "array" and
        (.layers | length) == 2 and
        ([.layers[].annotations["in-toto.io/predicate-type"]] | sort) ==
            ["https://slsa.dev/provenance/v1", "https://spdx.dev/Document"] and
        all(.layers[]; .mediaType == "application/vnd.in-toto+json")
    ' "$attestation_manifest" >/dev/null ||
        die "linux/$arch attestation predicate set is incomplete or unexpected"

    jq -e '
        all(.layers[];
            (.digest | type) == "string" and
            (.size | type) == "number" and
            .size > 0)
    ' "$attestation_manifest" >/dev/null ||
        die "linux/$arch attestation descriptors are malformed"

    jq -e '
        .config.mediaType == "application/vnd.oci.image.config.v1+json" and
        (.config.digest | type) == "string" and
        (.config.size | type) == "number" and
        .config.size > 0
    ' "$attestation_manifest" >/dev/null ||
        die "linux/$arch attestation config descriptor is malformed"
    attestation_config_digest=$(jq -er '.config.digest' "$attestation_manifest")
    attestation_config_size=$(jq -er '.config.size' "$attestation_manifest")
    verify_descriptor "$attestation_config_digest" "$attestation_config_size"
    attestation_config=$(blob_path "$attestation_config_digest")
    expected_attestation_diff_ids=$(jq -c '[.layers[].digest]' "$attestation_manifest")
    jq -e --argjson expected_diff_ids "$expected_attestation_diff_ids" '
        .architecture == "unknown" and
        .os == "unknown" and
        .config == {} and
        .rootfs.type == "layers" and
        (.rootfs.diff_ids | type) == "array" and
        .rootfs.diff_ids == $expected_diff_ids
    ' "$attestation_config" >/dev/null ||
        die "linux/$arch attestation config does not bind its predicate layers"

    for predicate_type in https://spdx.dev/Document https://slsa.dev/provenance/v1; do
        predicate_descriptors=()
        while IFS= read -r descriptor; do
            predicate_descriptors+=("$descriptor")
        done < <(jq -c --arg predicate "$predicate_type" '
            .layers[] | select(.annotations["in-toto.io/predicate-type"] == $predicate)
        ' "$attestation_manifest")
        ((${#predicate_descriptors[@]} == 1)) ||
            die "linux/$arch must have exactly one $predicate_type statement"
        predicate_descriptor=${predicate_descriptors[0]}
        predicate_digest=$(jq -er '.digest' <<<"$predicate_descriptor")
        predicate_size=$(jq -er '.size' <<<"$predicate_descriptor")
        verify_descriptor "$predicate_digest" "$predicate_size"
        predicate_body=$(blob_path "$predicate_digest")
        image_sha=${image_digest#sha256:}

        jq -e --arg predicate "$predicate_type" --arg image_sha "$image_sha" '
            ._type == "https://in-toto.io/Statement/v1" and
            .predicateType == $predicate and
            (.subject | type) == "array" and
            ((.subject | length) == 0 or
                all(.subject[]; .digest.sha256 == $image_sha))
        ' "$predicate_body" >/dev/null ||
            die "linux/$arch $predicate_type envelope does not match its image manifest"

        if [[ $predicate_type == https://spdx.dev/Document ]]; then
            jq -e '
                .predicate.SPDXID == "SPDXRef-DOCUMENT" and
                .predicate.spdxVersion == "SPDX-2.3" and
                .predicate.dataLicense == "CC0-1.0" and
                (.predicate.packages | type) == "array" and
                (.predicate.packages | length) > 0 and
                (.predicate.files | type) == "array" and
                (.predicate.files | length) > 0 and
                any(.predicate.packages[];
                    .name == "github.com/ericismyeldestson/chinese-poetry-api")
            ' "$predicate_body" >/dev/null || die "linux/$arch SPDX SBOM is incomplete"
            [[ -z $evidence_dir ]] || cp "$predicate_body" "$evidence_dir/sbom-$arch.spdx.json"
        else
            jq -e --arg revision "$expected_revision" --arg arch "$arch" \
                --arg encoded "platform=linux%2F$arch" '
                .predicate.buildDefinition.buildType ==
                    "https://github.com/moby/buildkit/blob/master/docs/attestations/slsa-definitions.md" and
                .predicate.buildDefinition.externalParameters.request.args[
                    "build-arg:VCS_REF"] == $revision and
                .predicate.buildDefinition.externalParameters.request.root.request.args[
                    "vcs:revision"] == $revision and
                .predicate.buildDefinition.externalParameters.request.root.request.args[
                    "vcs:source"] ==
                    "https://github.com/ericismyeldestson/chinese-poetry-api.git" and
                (.predicate.buildDefinition.resolvedDependencies | type) == "array" and
                (.predicate.buildDefinition.resolvedDependencies | length) >= 2 and
                all(.predicate.buildDefinition.resolvedDependencies[];
                    (.uri | type) == "string" and
                    (.digest.sha256 | type) == "string" and
                    (.digest.sha256 | test("^[0-9a-f]{64}$"))) and
                any(.predicate.buildDefinition.resolvedDependencies[];
                    .uri | contains($encoded)) and
                ([.predicate.buildDefinition.internalParameters.buildConfig.llbDefinition[] |
                    .op.platform? | select(. != null) |
                    select(.OS == "linux" and .Architecture == $arch)] | length) > 0
            ' "$predicate_body" >/dev/null || die "linux/$arch SLSA provenance contract failed"
            [[ -z $evidence_dir ]] || cp "$predicate_body" "$evidence_dir/provenance-$arch.json"
        fi
    done

    [[ -z $evidence_dir ]] || cp "$config" "$evidence_dir/image-config-$arch.json"
    [[ -z $evidence_dir ]] || cp "$image_manifest" "$evidence_dir/image-manifest-$arch.json"
    [[ -z $evidence_dir ]] ||
        cp "$attestation_manifest" "$evidence_dir/attestation-manifest-$arch.json"
    echo "verified linux/$arch image=$image_digest attestation=$attestation_digest"
done

echo "multi-platform OCI image, SPDX SBOMs, and SLSA provenance verified"
