#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "${script_dir}/.." && pwd)
contract=${DATA_CONTRACT_PATH:-${repo_root}/data/source-manifest.json}
database=
release_manifest=
source_report=

usage() {
    echo "usage: $0 [--contract-only] [--database PATH] [--source-report PATH] [--manifest PATH]" >&2
    exit 2
}

die() {
    echo "$*" >&2
    exit 1
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --contract-only) shift ;;
        --database) [ "$#" -ge 2 ] || usage; database=$2; shift 2 ;;
        --source-report) [ "$#" -ge 2 ] || usage; source_report=$2; shift 2 ;;
        --manifest) [ "$#" -ge 2 ] || usage; release_manifest=$2; shift 2 ;;
        *) usage ;;
    esac
done

sha256_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{ print $1 }'
    else
        shasum -a 256 "$1" | awk '{ print $1 }'
    fi
}

sqlite_uri() {
    path=$1
    case "$path" in
        /*) absolute=$path ;;
        *) absolute=$PWD/$path ;;
    esac
    encoded=$(printf '%s' "$absolute" | sed \
        -e 's/%/%25/g' -e 's/ /%20/g' -e 's/?/%3F/g' -e 's/#/%23/g')
    printf 'file:%s?mode=ro&immutable=1\n' "$encoded"
}

command -v jq >/dev/null 2>&1 || die "jq is required"
[ -f "$contract" ] || die "missing data contract: $contract"

contract_version=$(jq -er '.contract_version' "$contract")
source_repository=$(jq -er '.source.repository' "$contract")
source_commit=$(jq -er '.source.commit' "$contract")
source_license=$(jq -er '.source.license' "$contract")
pipeline_repository=$(jq -er '.pipeline.repository' "$contract")
schema_version=$(jq -er '.pipeline.schema_version' "$contract")
minimum_authors=$(jq -er '.quality_contract.min_authors_per_language' "$contract")
expected_poems=$(jq -er '.quality_contract.expected_products.poems_per_language' "$contract")
expected_witnesses=$(jq -er '.quality_contract.expected_products.source_witnesses_per_language' "$contract")
canonical_id_prefix=$(jq -er '.quality_contract.canonical_id_prefix' "$contract")
canonical_author_id_prefix=$(jq -er '.quality_contract.canonical_author_id_prefix' "$contract")
expected_database_sha256=$(jq -er '.quality_contract.expected_database_sha256' "$contract")
expected_file_decisions=$(jq -er '.quality_contract.expected_file_decisions' "$contract")
expected_file_decisions_sha256=$(jq -er '.quality_contract.expected_file_decisions_sha256' "$contract")
integrity_check=$(jq -er '.quality_contract.integrity_check' "$contract")

[ "$contract_version" = "1" ] || die "unsupported data contract version: $contract_version"
printf '%s\n' "$source_commit" | grep -Eq '^[0-9a-f]{40}$' || die "source commit must be a full SHA"
[ "$source_repository" = "https://github.com/chinese-poetry/chinese-poetry.git" ] || die "unexpected data source repository"
[ "$source_license" = "MIT" ] || die "unexpected data source license"
[ "$pipeline_repository" = "https://github.com/ericismyeldestson/chinese-poetry-api.git" ] || die "unexpected pipeline repository"
[ "$schema_version" = "2" ] || die "this release pipeline requires schema version 2"
if ! {
    [ "$expected_poems" -ge 1 ] &&
        [ "$expected_witnesses" -ge "$expected_poems" ] &&
        [ "$minimum_authors" -ge 1 ]
}; then
    die "invalid quality thresholds"
fi
[ "$integrity_check" = "quick_check" ] || die "unsupported integrity check: $integrity_check"
printf '%s\n' "$expected_file_decisions_sha256" | grep -Eq '^[0-9a-f]{64}$' ||
    die "source file-decision digest must be SHA-256"
printf '%s\n' "$canonical_id_prefix" | grep -Eq '^cpa:poem:v[0-9]+:sha256:$' ||
    die "canonical identity prefix is invalid"
printf '%s\n' "$canonical_author_id_prefix" | grep -Eq '^cpa:author:v[0-9]+:sha256:$' ||
    die "canonical author identity prefix is invalid"
printf '%s\n' "$expected_database_sha256" | grep -Eq '^[0-9a-f]{64}$' ||
    die "expected database digest must be SHA-256"
jq -e '
    (.quality_contract.expected_logical_digests | keys) == [
      "author_products_zh_hans", "author_products_zh_hant",
      "poem_products_zh_hans", "poem_products_zh_hant",
      "source_rejections", "source_witnesses_zh_hans",
      "source_witnesses_zh_hant", "taxonomy_zh_hans", "taxonomy_zh_hant"
    ] and
    all(.quality_contract.expected_logical_digests[];
      type == "string" and test("^[0-9a-f]{64}$"))
' "$contract" >/dev/null || die "logical digest contract is missing, malformed, or has unexpected keys"
jq -e '.quality_contract.required_languages == ["zh-Hans", "zh-Hant"]' "$contract" >/dev/null ||
    die "required language contract must be exactly zh-Hans and zh-Hant"

expected_tables='metadata source_rejections dynasties_zh_hans authors_zh_hans poetry_types_zh_hans poems_zh_hans poem_sources_zh_hans poems_fts_zh_hans dynasties_zh_hant authors_zh_hant poetry_types_zh_hant poems_zh_hant poem_sources_zh_hant poems_fts_zh_hant'
required_tables=$(jq -er '.quality_contract.required_tables | join(" ")' "$contract")
[ "$required_tables" = "$expected_tables" ] || die "schema v2 contract must list the expected 14 tables in canonical order"

gitmodules_url=$(git -C "$repo_root" config -f .gitmodules --get submodule.poetry-data.url)
[ "$gitmodules_url" = "$source_repository" ] || die ".gitmodules source URL does not match the contract"
tree_commit=$(git -C "$repo_root" ls-tree HEAD poetry-data | awk '{ print $3 }')
[ "$tree_commit" = "$source_commit" ] || die "submodule SHA does not match the contract"
if [ -e "${repo_root}/poetry-data/.git" ]; then
    checkout_commit=$(git -C "${repo_root}/poetry-data" rev-parse HEAD)
    [ "$checkout_commit" = "$source_commit" ] || die "checked-out source data differs from the pinned gitlink"
    [ -z "$(git -C "${repo_root}/poetry-data" status --porcelain)" ] || die "checked-out source data is dirty"
fi

if [ -n "$source_report" ]; then
    [ -f "$source_report" ] || die "missing source report: $source_report"
    [ "$(jq -er '.schema_version' "$source_report")" = "1" ] || die "unsupported source report schema"
    jq -e '
        (.files | type == "array") and
        (.totals | type == "object") and
        all(.files[];
            (.dataset_key | type == "string" and test("^[a-z0-9][a-z0-9._-]*$")) and
            (.source_path | type == "string" and endswith(".json") and
                (contains("\\") | not) and (startswith("/") | not) and
                (startswith("../") | not) and (contains("/../") | not)) and
            (.action == "loaded" or .action == "excluded") and
            (.total_records | type == "number" and . >= 0 and floor == .) and
            (.accepted_records | type == "number" and . >= 0 and floor == .) and
            (.rejected_records | type == "number" and . >= 0 and floor == .) and
            (if .action == "loaded" then
                ((.reason // "") == "") and
                (.total_records == (.accepted_records + .rejected_records))
             else
                (.reason == "listed in dataset excludes" or
                 .reason == "upstream author metadata (datas.json historically lists authors.song.json)") and
                (.total_records == 0 and .accepted_records == 0 and .rejected_records == 0)
             end)
        )
    ' "$source_report" >/dev/null || die "source report file decisions violate the schema or reason allowlist"
    jq -e '
        ([.files[] | [.dataset_key, .source_path]]) as $keys |
        ($keys == ($keys | sort)) and
        (($keys | unique | length) == ($keys | length)) and
        (.totals.total_records == ([.files[].total_records] | add // 0)) and
        (.totals.accepted_records == ([.files[].accepted_records] | add // 0)) and
        (.totals.rejected_records == ([.files[].rejected_records] | add // 0)) and
        (.totals.excluded_files == ([.files[] | select(.action == "excluded")] | length)) and
        (.totals.total_records == (.totals.accepted_records + .totals.rejected_records))
    ' "$source_report" >/dev/null || die "source report ordering, uniqueness, or totals are invalid"
    [ "$(jq -cS '.totals' "$source_report")" = "$(jq -cS '.quality_contract.expected_source_totals' "$contract")" ] ||
        die "source report totals differ from the pinned-source baseline"
    [ "$(jq -er '.files | length' "$source_report")" = "$expected_file_decisions" ] ||
        die "source report file-decision count differs from the pinned-source baseline"
    file_decisions_sha=$(jq -r '.files[] | [.dataset_key,.source_path,.action,(.reason//""),.total_records,.accepted_records,.rejected_records] | @tsv' "$source_report" |
        sha256_file /dev/stdin)
    [ "$file_decisions_sha" = "$expected_file_decisions_sha256" ] ||
        die "source report file-decision digest differs from the pinned-source baseline"
fi

if [ -n "$release_manifest" ]; then
    [ -n "$source_report" ] || die "release manifest verification requires --source-report"
    [ -f "$release_manifest" ] || die "missing release manifest: $release_manifest"
    [ "$(jq -er '.source.repository' "$release_manifest")" = "$source_repository" ] || die "release manifest source repository mismatch"
    [ "$(jq -er '.source.commit' "$release_manifest")" = "$source_commit" ] || die "release manifest source SHA mismatch"
    [ "$(jq -er '.source.license' "$release_manifest")" = "$source_license" ] || die "release manifest source license mismatch"
    [ "$(jq -er '.pipeline.repository' "$release_manifest")" = "$pipeline_repository" ] || die "release manifest pipeline repository mismatch"
    pipeline_commit=$(jq -er '.pipeline.commit' "$release_manifest")
    printf '%s\n' "$pipeline_commit" | grep -Eq '^[0-9a-f]{40}$' || die "release manifest pipeline commit must be a full SHA"
    [ "$(jq -er '.schema_version' "$release_manifest")" = "$schema_version" ] || die "release manifest schema version mismatch"
    [ "$(jq -er '.database.asset' "$release_manifest")" = "poetry.db.gz" ] || die "release manifest database asset mismatch"
    [ "$(jq -er '.database.uncompressed_asset' "$release_manifest")" = "poetry.db" ] || die "release manifest raw database asset mismatch"
    [ "$(jq -er '.database.sha256' "$release_manifest")" = "$expected_database_sha256" ] || die "release manifest database digest mismatch"
    if [ -n "$database" ]; then
        [ "$(jq -er '.database.sha256' "$release_manifest")" = "$(sha256_file "$database")" ] ||
            die "release manifest database digest does not match the supplied database"
    fi
    release_tag=$(jq -er '.release_tag' "$release_manifest")
    printf '%s\n' "$release_tag" | grep -Eq '^v[0-9]+\.[0-9]+\.0$' || die "release manifest tag is not a stable data tag"
    if [ -n "${EXPECTED_RELEASE_TAG:-}" ]; then
        [ "$release_tag" = "$EXPECTED_RELEASE_TAG" ] || die "release manifest tag does not match the expected release tag"
    fi
    jq -e '.generated_at | fromdateiso8601' "$release_manifest" >/dev/null || die "release manifest generated_at must be RFC 3339 UTC"
    jq -e '.statistics | all(.[]; type == "number" and . >= 0)' "$release_manifest" >/dev/null || die "release manifest statistics are invalid"
    [ "$(jq -er '.source_report.asset' "$release_manifest")" = "$(basename -- "$source_report")" ] || die "release manifest source report asset mismatch"
    [ "$(jq -er '.source_report.schema_version' "$release_manifest")" = "$(jq -er '.schema_version' "$source_report")" ] || die "release manifest source report schema mismatch"
    [ "$(jq -er '.source_report.sha256' "$release_manifest")" = "$(sha256_file "$source_report")" ] || die "release manifest source report checksum mismatch"
    [ "$(jq -cS '.source_report.totals' "$release_manifest")" = "$(jq -cS '.totals' "$source_report")" ] || die "release manifest source report totals mismatch"
fi

if [ -n "$database" ]; then
    command -v sqlite3 >/dev/null 2>&1 || die "sqlite3 is required"
    [ -s "$database" ] || die "database is missing or empty: $database"
    [ "$(sha256_file "$database")" = "$expected_database_sha256" ] ||
        die "raw database digest differs from the reviewed pinned-source artifact"
    database_uri=$(sqlite_uri "$database")
    [ "$(sqlite3 "$database_uri" "PRAGMA ${integrity_check};")" = "ok" ] || die "database integrity check failed"
    [ -z "$(sqlite3 "$database_uri" 'PRAGMA foreign_key_check;')" ] || die "database foreign-key check failed"
    [ "$(sqlite3 "$database_uri" "SELECT value FROM metadata WHERE key='schema_version';")" = "$schema_version" ] || die "database schema version mismatch"

    for table in $required_tables; do
        present=$(sqlite3 "$database_uri" "SELECT count(*) FROM sqlite_master WHERE name='$table' AND type IN ('table','view');")
        [ "$present" = "1" ] || die "required table missing: $table"
    done

    invalid_rejections=$(sqlite3 "$database_uri" \
        "SELECT count(*) FROM source_rejections WHERE locator_id IS NULL OR trim(locator_id) = '' OR dataset_key IS NULL OR trim(dataset_key) = '' OR source_path IS NULL OR trim(source_path) = '' OR source_record_index < 0 OR stage IS NULL OR trim(stage) = '' OR reason IS NULL OR trim(reason) = '';" )
    [ "$invalid_rejections" = "0" ] || die "source rejection rows have missing locator, stage, or reason: $invalid_rejections"
    duplicate_rejections=$(sqlite3 "$database_uri" \
        'SELECT count(*) FROM (SELECT locator_id FROM source_rejections GROUP BY locator_id HAVING count(*) > 1);')
    [ "$duplicate_rejections" = "0" ] || die "duplicate source rejection locators: $duplicate_rejections"

    jq -r '.quality_contract.expected_rejections[] | [.stage, .reason, (.count|tostring)] | @tsv' "$contract" |
        while IFS="$(printf '\t')" read -r stage reason expected; do
            actual=$(sqlite3 "$database_uri" \
                "SELECT count(*) FROM source_rejections WHERE stage='$(printf "%s" "$stage" | sed "s/'/''/g")' AND reason='$(printf "%s" "$reason" | sed "s/'/''/g")';")
            [ "$actual" = "$expected" ] || die "source rejection baseline differs for $stage/$reason"
        done
    expected_rejection_kinds=$(jq -er '.quality_contract.expected_rejections | length' "$contract")
    actual_rejection_kinds=$(sqlite3 "$database_uri" 'SELECT count(*) FROM (SELECT 1 FROM source_rejections GROUP BY stage,reason);')
    [ "$actual_rejection_kinds" = "$expected_rejection_kinds" ] || die "unexpected source rejection reason or stage"

    for suffix in zh_hans zh_hant; do
        poems=$(sqlite3 "$database_uri" "SELECT count(*) FROM poems_${suffix};")
        authors=$(sqlite3 "$database_uri" "SELECT count(*) FROM authors_${suffix};")
        [ "$poems" = "$expected_poems" ] || die "poem count differs from the pinned-source baseline for $suffix"
        [ "$authors" -ge "$minimum_authors" ] || die "author count below contract for $suffix"

        expected_authors=$(jq -er ".quality_contract.expected_products.authors_${suffix}" "$contract")
        [ "$authors" = "$expected_authors" ] || die "author count differs from the pinned-source baseline for $suffix"

        author_identity_column=$(sqlite3 "$database_uri" \
            "SELECT count(*) FROM pragma_table_info('authors_${suffix}') WHERE name='canonical_id' AND \"notnull\"=1;")
        [ "$author_identity_column" = "1" ] || die "authors_${suffix}.canonical_id must be NOT NULL"
        author_identity_unique=$(sqlite3 "$database_uri" \
            "SELECT count(*) FROM pragma_index_list('authors_${suffix}') il WHERE il.\"unique\"=1 AND (SELECT count(*) FROM pragma_index_info(il.name))=1 AND EXISTS (SELECT 1 FROM pragma_index_info(il.name) WHERE name='canonical_id');")
        [ "$author_identity_unique" = "1" ] || die "authors_${suffix}.canonical_id must have a single-column UNIQUE constraint"

        missing_identity=$(sqlite3 "$database_uri" \
            "SELECT count(*) FROM poems_${suffix} WHERE canonical_id IS NULL OR canonical_id = '' OR canonical_fingerprint IS NULL OR canonical_fingerprint = '';")
        [ "$missing_identity" = "0" ] || die "poems without canonical identity in $suffix: $missing_identity"

        malformed_identity=$(sqlite3 "$database_uri" \
            "SELECT count(*) FROM poems_${suffix} WHERE canonical_id NOT GLOB '${canonical_id_prefix}*' OR length(canonical_id) != length('${canonical_id_prefix}') + 64 OR substr(canonical_id, length('${canonical_id_prefix}') + 1) GLOB '*[^0-9a-f]*' OR canonical_fingerprint GLOB '*[^0-9a-f]*' OR length(canonical_fingerprint) != 128;")
        [ "$malformed_identity" = "0" ] || die "malformed canonical identity in $suffix: $malformed_identity"

        unsafe_id=$(sqlite3 "$database_uri" \
            "SELECT count(*) FROM poems_${suffix} WHERE id < 1 OR id > 9007199254740991;")
        [ "$unsafe_id" = "0" ] || die "poem IDs outside the JavaScript-safe range in $suffix: $unsafe_id"

        # Recompute the governed public ID from the first 64 digest bits. The
        # algorithm keeps the low 53 bits (mapping zero to one), so every value
        # is exactly representable by awk and by JavaScript clients.
        if ! sqlite3 -separator '|' "$database_uri" "SELECT id, canonical_id FROM poems_${suffix} ORDER BY id;" |
            awk -F '|' -v prefix="$canonical_id_prefix" '
                function hv(c, p) { p=index("0123456789abcdef", tolower(c)); return p ? p-1 : -1 }
                {
                    hex=substr($2, length(prefix)+1)
                    value=hv(substr(hex,3,1)) % 2
                    for (i=4; i<=16; i++) value=value*16+hv(substr(hex,i,1))
                    if (value == 0) value=1
                    if (($1+0) != value) exit 1
                }
            '; then
            die "poem ID formula mismatch in $suffix"
        fi

        bad_text=$(sqlite3 "$database_uri" \
            "SELECT (SELECT count(*) FROM poems_${suffix} WHERE instr(title, char(65533)) > 0 OR instr(content, char(65533)) > 0 OR content = '[]') + (SELECT count(*) FROM authors_${suffix} WHERE instr(name, char(65533)) > 0);")
        [ "$bad_text" = "0" ] || die "replacement characters or empty content remain in $suffix: $bad_text"

        invalid_author_relation=$(sqlite3 "$database_uri" \
			"SELECT count(*) FROM poems_${suffix} p LEFT JOIN authors_${suffix} a ON a.id=p.author_id WHERE p.author_id IS NULL OR a.id IS NULL OR p.dynasty_id IS NOT a.dynasty_id;")
		[ "$invalid_author_relation" = "0" ] || die "poem/author relationship is missing or has a dynasty identity mismatch in $suffix: $invalid_author_relation"

		malformed_author_identity=$(sqlite3 "$database_uri" \
			"SELECT count(*) FROM authors_${suffix} WHERE canonical_id NOT GLOB '${canonical_author_id_prefix}*' OR length(canonical_id) != length('${canonical_author_id_prefix}') + 64 OR substr(canonical_id, length('${canonical_author_id_prefix}') + 1) GLOB '*[^0-9a-f]*' OR id < 1 OR id > 9007199254740991;")
		[ "$malformed_author_identity" = "0" ] || die "malformed canonical author identity in $suffix: $malformed_author_identity"
		if ! sqlite3 -separator '|' "$database_uri" "SELECT id, canonical_id FROM authors_${suffix} ORDER BY id;" |
			awk -F '|' -v prefix="$canonical_author_id_prefix" '
				function hv(c, p) { p=index("0123456789abcdef", tolower(c)); return p ? p-1 : -1 }
				{
					hex=substr($2, length(prefix)+1)
					value=hv(substr(hex,3,1)) % 2
					for (i=4; i<=16; i++) value=value*16+hv(substr(hex,i,1))
					if (value == 0) value=1
					if (($1+0) != value) exit 1
				}
			'; then
			die "author ID formula mismatch in $suffix"
		fi

        missing_source=$(sqlite3 "$database_uri" \
            "SELECT count(*) FROM poems_${suffix} p WHERE NOT EXISTS (SELECT 1 FROM poem_sources_${suffix} s WHERE s.poem_id = p.id);")
        [ "$missing_source" = "0" ] || die "poems without source provenance in $suffix: $missing_source"

        orphan_source=$(sqlite3 "$database_uri" \
            "SELECT count(*) FROM poem_sources_${suffix} s WHERE NOT EXISTS (SELECT 1 FROM poems_${suffix} p WHERE p.id = s.poem_id);")
        [ "$orphan_source" = "0" ] || die "orphan source rows in $suffix: $orphan_source"

        poem_product_digest=$(sqlite3 "$database_uri" \
            "SELECT canonical_id || '|' || canonical_fingerprint || '|' || id || '|' || ifnull(type_id,'') || '|' || ifnull(author_id,'') || '|' || ifnull(dynasty_id,'') || '|' || hex(title) || '|' || hex(content) || '|' || content_hash FROM poems_${suffix} ORDER BY canonical_id;" |
            sha256_file /dev/stdin)
        [ "$poem_product_digest" = "$(jq -er ".quality_contract.expected_logical_digests.poem_products_${suffix}" "$contract")" ] ||
            die "localized poem product digest differs from the reviewed baseline for $suffix"
        author_product_digest=$(sqlite3 "$database_uri" \
            "SELECT canonical_id || '|' || id || '|' || dynasty_id || '|' || hex(name) || '|' || hex(ifnull(description,'')) FROM authors_${suffix} ORDER BY canonical_id;" |
            sha256_file /dev/stdin)
        [ "$author_product_digest" = "$(jq -er ".quality_contract.expected_logical_digests.author_products_${suffix}" "$contract")" ] ||
            die "localized author product digest differs from the reviewed baseline for $suffix"
        source_witness_digest=$(sqlite3 "$database_uri" \
            "SELECT s.locator_id || '|' || p.canonical_id || '|' || hex(ifnull(s.source_id,'')) || '|' || hex(s.dataset_key) || '|' || hex(s.source_path) || '|' || s.source_record_index FROM poem_sources_${suffix} s JOIN poems_${suffix} p ON p.id=s.poem_id ORDER BY s.locator_id;" |
            sha256_file /dev/stdin)
        [ "$source_witness_digest" = "$(jq -er ".quality_contract.expected_logical_digests.source_witnesses_${suffix}" "$contract")" ] ||
            die "source locator-to-product digest differs from the reviewed baseline for $suffix"
        taxonomy_digest=$(sqlite3 "$database_uri" \
            "SELECT row FROM (SELECT 'D|' || id || '|' || hex(name) || '|' || hex(ifnull(name_en,'')) || '|' || ifnull(start_year,'') || '|' || ifnull(end_year,'') AS row, 1 AS kind, id AS sort_id FROM dynasties_${suffix} UNION ALL SELECT 'T|' || id || '|' || hex(name) || '|' || hex(category) || '|' || ifnull(lines,'') || '|' || ifnull(chars_per_line,'') || '|' || hex(ifnull(description,'')), 2, id FROM poetry_types_${suffix}) ORDER BY kind, sort_id;" |
            sha256_file /dev/stdin)
        [ "$taxonomy_digest" = "$(jq -er ".quality_contract.expected_logical_digests.taxonomy_${suffix}" "$contract")" ] ||
            die "localized dynasty/type taxonomy digest differs from the reviewed baseline for $suffix"
    done

    source_rejection_digest=$(sqlite3 "$database_uri" \
        "SELECT locator_id || '|' || hex(ifnull(source_id,'')) || '|' || hex(dataset_key) || '|' || hex(source_path) || '|' || source_record_index || '|' || hex(stage) || '|' || hex(reason) FROM source_rejections ORDER BY locator_id;" |
        sha256_file /dev/stdin)
    [ "$source_rejection_digest" = "$(jq -er '.quality_contract.expected_logical_digests.source_rejections' "$contract")" ] ||
        die "source rejection ledger digest differs from the reviewed baseline"

    hans_only=$(sqlite3 "$database_uri" \
        'SELECT count(*) FROM (SELECT canonical_id FROM poems_zh_hans EXCEPT SELECT canonical_id FROM poems_zh_hant);')
    hant_only=$(sqlite3 "$database_uri" \
        'SELECT count(*) FROM (SELECT canonical_id FROM poems_zh_hant EXCEPT SELECT canonical_id FROM poems_zh_hans);')
    if [ "$hans_only" != "0" ] || [ "$hant_only" != "0" ]; then
        die "simplified/traditional canonical identity sets differ (Hans-only=$hans_only, Hant-only=$hant_only)"
    fi

    cross_language_identity_mismatch=$(sqlite3 "$database_uri" \
		'SELECT count(*) FROM poems_zh_hans h JOIN poems_zh_hant t USING(canonical_id) WHERE h.id != t.id OR h.canonical_fingerprint != t.canonical_fingerprint OR h.author_id IS NOT t.author_id OR h.dynasty_id IS NOT t.dynasty_id OR h.type_id IS NOT t.type_id;')
    [ "$cross_language_identity_mismatch" = "0" ] ||
        die "simplified/traditional public IDs or fingerprints differ: $cross_language_identity_mismatch"

	hans_author_only=$(sqlite3 "$database_uri" \
		'SELECT count(*) FROM (SELECT canonical_id FROM authors_zh_hans EXCEPT SELECT canonical_id FROM authors_zh_hant);')
	hant_author_only=$(sqlite3 "$database_uri" \
		'SELECT count(*) FROM (SELECT canonical_id FROM authors_zh_hant EXCEPT SELECT canonical_id FROM authors_zh_hans);')
	if [ "$hans_author_only" != "0" ] || [ "$hant_author_only" != "0" ]; then
		die "simplified/traditional canonical author sets differ"
	fi
	cross_language_author_mismatch=$(sqlite3 "$database_uri" \
		'SELECT count(*) FROM authors_zh_hans h JOIN authors_zh_hant t USING(canonical_id) WHERE h.id != t.id OR h.dynasty_id IS NOT t.dynasty_id;')
	[ "$cross_language_author_mismatch" = "0" ] ||
		die "simplified/traditional author IDs or dynasty relations differ: $cross_language_author_mismatch"

    rejected_and_accepted=$(sqlite3 "$database_uri" \
        'SELECT count(*) FROM source_rejections r WHERE EXISTS (SELECT 1 FROM poem_sources_zh_hans s WHERE s.locator_id = r.locator_id) OR EXISTS (SELECT 1 FROM poem_sources_zh_hant s WHERE s.locator_id = r.locator_id);')
    [ "$rejected_and_accepted" = "0" ] || die "source locators appear in both accepted and rejected ledgers: $rejected_and_accepted"

    hans_source_only=$(sqlite3 "$database_uri" \
        'SELECT count(*) FROM (SELECT locator_id FROM poem_sources_zh_hans EXCEPT SELECT locator_id FROM poem_sources_zh_hant);')
    hant_source_only=$(sqlite3 "$database_uri" \
        'SELECT count(*) FROM (SELECT locator_id FROM poem_sources_zh_hant EXCEPT SELECT locator_id FROM poem_sources_zh_hans);')
    if [ "$hans_source_only" != "0" ] || [ "$hant_source_only" != "0" ]; then
        die "simplified/traditional source locator sets differ (Hans-only=$hans_source_only, Hant-only=$hant_source_only)"
    fi

    cross_language_locator_mismatch=$(sqlite3 "$database_uri" \
        'SELECT count(*) FROM poem_sources_zh_hans hs JOIN poem_sources_zh_hant ts USING(locator_id) JOIN poems_zh_hans hp ON hp.id=hs.poem_id JOIN poems_zh_hant tp ON tp.id=ts.poem_id WHERE hp.canonical_id != tp.canonical_id OR hs.source_id IS NOT ts.source_id OR hs.dataset_key != ts.dataset_key OR hs.source_path != ts.source_path OR hs.source_record_index != ts.source_record_index;')
    [ "$cross_language_locator_mismatch" = "0" ] ||
        die "simplified/traditional locator provenance differs: $cross_language_locator_mismatch"

    mislabeled_poetry=$(sqlite3 "$database_uri" \
        "SELECT (SELECT count(*) FROM poetry_types_zh_hans WHERE category='唐诗') + (SELECT count(*) FROM poetry_types_zh_hant WHERE category='唐詩');")
    [ "$mislabeled_poetry" = "0" ] || die "era-specific 唐诗 category remains in the form taxonomy"

	jq -r '.quality_contract.expected_poems_by_dynasty_zh_hans | to_entries[] | [.key, (.value|tostring)] | @tsv' "$contract" |
		while IFS="$(printf '\t')" read -r dynasty expected; do
			actual=$(sqlite3 "$database_uri" \
				"SELECT count(*) FROM poems_zh_hans p JOIN dynasties_zh_hans d ON d.id=p.dynasty_id WHERE d.name='$(printf "%s" "$dynasty" | sed "s/'/''/g")';")
			[ "$actual" = "$expected" ] || die "poem count differs from dynasty baseline for $dynasty"
		done

	jq -r '.quality_contract.expected_categories_zh_hans | to_entries[] | [.key, (.value|tostring)] | @tsv' "$contract" |
		while IFS="$(printf '\t')" read -r category expected; do
			actual=$(sqlite3 "$database_uri" \
				"SELECT count(*) FROM poems_zh_hans p JOIN poetry_types_zh_hans t ON t.id=p.type_id WHERE t.category='$(printf "%s" "$category" | sed "s/'/''/g")';")
			[ "$actual" = "$expected" ] || die "poem count differs from category baseline for $category"
		done

    if [ -n "$source_report" ]; then
        reported_accepted=$(jq -er '.totals.accepted_records' "$source_report")
        reported_rejected=$(jq -er '.totals.rejected_records' "$source_report")
        hans_sources=$(sqlite3 "$database_uri" 'SELECT count(*) FROM poem_sources_zh_hans;')
        hant_sources=$(sqlite3 "$database_uri" 'SELECT count(*) FROM poem_sources_zh_hant;')
        rejection_rows=$(sqlite3 "$database_uri" 'SELECT count(*) FROM source_rejections;')
        [ "$hans_sources" = "$expected_witnesses" ] || die "Hans source witness count differs from the pinned-source baseline"
        [ "$hant_sources" = "$expected_witnesses" ] || die "Hant source witness count differs from the pinned-source baseline"
        [ "$hans_sources" = "$reported_accepted" ] || die "Hans source witness count differs from the source report"
        [ "$hant_sources" = "$reported_accepted" ] || die "Hant source witness count differs from the source report"
        [ "$rejection_rows" = "$reported_rejected" ] || die "source rejection count differs from the source report"
    fi

    if [ -n "$release_manifest" ]; then
        for suffix in zh_hans zh_hant; do
            poems=$(sqlite3 "$database_uri" "SELECT count(*) FROM poems_${suffix};")
            authors=$(sqlite3 "$database_uri" "SELECT count(*) FROM authors_${suffix};")
            [ "$(jq -er ".statistics.poems_${suffix}" "$release_manifest")" = "$poems" ] || die "release manifest poem count mismatch for $suffix"
            [ "$(jq -er ".statistics.authors_${suffix}" "$release_manifest")" = "$authors" ] || die "release manifest author count mismatch for $suffix"
        done
    fi
fi

echo "data contract verified"
