#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "${script_dir}/.." && pwd)
inventory=${repo_root}/licenses/go-dependencies.csv
bundle=${repo_root}/licenses/go-dependencies
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/chinese-poetry-license.XXXXXX")
trap 'rm -rf -- "$work_dir"' EXIT HUP INT TERM

die() {
    printf 'ERROR: %s\n' "$*" >&2
    exit 1
}

sha256_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{ print $1 }'
    else
        shasum -a 256 "$1" | awk '{ print $1 }'
    fi
}

[ -s "$inventory" ] || die "missing Go dependency license inventory"
[ -d "$bundle" ] || die "missing Go dependency license bundle"

if [ -n "${GO_LICENSES_BIN:-}" ]; then
    "$GO_LICENSES_BIN" report ./cmd/server >"$work_dir/current.csv"
else
    go run github.com/google/go-licenses@v1.6.0 report ./cmd/server >"$work_dir/current.csv"
fi
LC_ALL=C sort "$inventory" >"$work_dir/expected.sorted"
LC_ALL=C sort "$work_dir/current.csv" >"$work_dir/current.sorted"
if ! cmp -s "$work_dir/expected.sorted" "$work_dir/current.sorted"; then
    diff -u "$work_dir/expected.sorted" "$work_dir/current.sorted" >&2 || true
    die "reachable server dependency/license inventory drifted"
fi

if grep -Eq ',(GPL-2\.0[^,]*|Unknown)(,|$)' "$inventory"; then
    die "a GPL-2.0-only or unidentified dependency license is reachable"
fi
if go mod graph | grep -Eq 'github\.com/(liuzl/(gocc|da|cedar-go)|adamzy/cedar-go)'; then
    die "the incompatible legacy Chinese conversion dependency chain returned"
fi

cut -d, -f1 "$inventory" | while IFS= read -r module; do
    module_dir=$bundle/$module
    [ -d "$module_dir" ] || die "license directory is missing for $module"
    if ! find "$module_dir" -maxdepth 1 -type f \
        \( -iname 'license' -o -iname 'license.*' -o -iname 'license_*' \
        -o -iname 'unlicense' -o -iname 'notice' -o -iname 'notice.*' \) \
        | grep -q .; then
        die "license or notice text is missing for $module"
    fi
done

cmp -s "$repo_root/licenses/hanconv-MIT.txt" \
    "$bundle/github.com/fhluo/hanconv/go/LICENSE" ||
    die "the copied hanconv license differs from the reachable module"

hanconv_dir=$(go list -m -f '{{.Dir}}' github.com/fhluo/hanconv/go)
dictionary_dir=$hanconv_dir/dict/data
dictionary_manifest=$repo_root/licenses/opencc-dictionaries.SHA256
[ "$(wc -l < "$dictionary_manifest" | tr -d ' ')" = "13" ] ||
    die "the pinned OpenCC dictionary manifest must contain 13 files"
while IFS=' ' read -r expected_digest name expected_blob; do
    case "$name" in
        ''|*/*|*..*) die "invalid dictionary name in manifest: $name" ;;
    esac
    dictionary=$dictionary_dir/$name
    [ -f "$dictionary" ] || die "reachable hanconv dictionary is missing: $name"
    [ "$(sha256_file "$dictionary")" = "$expected_digest" ] ||
        die "reachable hanconv dictionary digest drifted: $name"
    [ "git-blob=$(git hash-object "$dictionary")" = "$expected_blob" ] ||
        die "reachable hanconv dictionary no longer matches the audited OpenCC blob: $name"
done < "$dictionary_manifest"
cmp -s "$repo_root/licenses/opencc-dictionaries-Apache-2.0.txt" \
    "$dictionary_dir/LICENSE" ||
    die "the copied OpenCC dictionary license differs from the reachable module"
cmp -s "$repo_root/NOTICE" \
    "$bundle/github.com/ericismyeldestson/chinese-poetry-api/NOTICE" ||
    die "the bundled project notice is stale"

for source_module in \
    github.com/go-sql-driver/mysql \
    github.com/hashicorp/golang-lru/v2
do
    source_dir=$bundle/$source_module
    [ -f "$source_dir/go.mod" ] || die "MPL source bundle is missing for $source_module"
    module_dir=$(go list -m -f '{{.Dir}}' "$source_module")
    diff -qr "$module_dir" "$source_dir" >/dev/null ||
        die "MPL source bundle differs from the exact selected module: $source_module"
done

printf 'license contract verified\n'
