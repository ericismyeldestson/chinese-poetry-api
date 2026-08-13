#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "${script_dir}/.." && pwd)
startup=${script_dir}/startup.sh
contract=${repo_root}/data/source-manifest.json

fail() {
    echo "startup regression failed: $*" >&2
    exit 1
}

for command in curl gzip jq sqlite3; do
    command -v "$command" >/dev/null 2>&1 || fail "$command is required"
done

sha256_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{ print $1 }'
    else
        shasum -a 256 "$1" | awk '{ print $1 }'
    fi
}

sqlite_query_sha() {
    sqlite3 "$1" "$2" | sha256_file /dev/stdin
}

tmp_root=$(mktemp -d "${TMPDIR:-/tmp}/poetry-startup-test.XXXXXX")
cleanup() {
    rm -rf -- "$tmp_root"
}
trap cleanup EXIT HUP INT TERM

create_fixture() {
    target=$1
    schema=$2
    sqlite3 "$target" <<SQL
PRAGMA foreign_keys = ON;
CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO metadata(key, value) VALUES ('schema_version', '$schema');
CREATE TABLE source_rejections (
    id INTEGER PRIMARY KEY,
    locator_id TEXT NOT NULL UNIQUE,
    source_id TEXT,
    dataset_key TEXT NOT NULL,
    source_path TEXT NOT NULL,
    source_record_index INTEGER NOT NULL,
    stage TEXT NOT NULL,
    reason TEXT NOT NULL
);
INSERT INTO source_rejections(id,locator_id,source_id,dataset_key,source_path,source_record_index,stage,reason) VALUES (1, 'source:rejected:1', NULL, 'fixture', 'fixture/poems.json', 1, 'load', 'fixture rejection');

CREATE TABLE dynasties_zh_hans (id INTEGER PRIMARY KEY, name TEXT NOT NULL, name_en TEXT, start_year INTEGER, end_year INTEGER);
CREATE TABLE authors_zh_hans (id INTEGER PRIMARY KEY, canonical_id TEXT NOT NULL UNIQUE, name TEXT NOT NULL, dynasty_id INTEGER NOT NULL, description TEXT);
CREATE TABLE poetry_types_zh_hans (id INTEGER PRIMARY KEY, name TEXT NOT NULL, category TEXT NOT NULL, lines INTEGER, chars_per_line INTEGER, description TEXT);
CREATE TABLE poems_zh_hans (
    id INTEGER PRIMARY KEY,
    canonical_id TEXT NOT NULL UNIQUE,
    canonical_fingerprint TEXT NOT NULL,
    dynasty_id INTEGER,
    author_id INTEGER,
    type_id INTEGER,
    title TEXT NOT NULL,
    content TEXT NOT NULL
    ,content_hash TEXT NOT NULL
);
CREATE TABLE poem_sources_zh_hans (
    id INTEGER PRIMARY KEY,
    poem_id INTEGER NOT NULL,
    locator_id TEXT NOT NULL UNIQUE,
    source_id TEXT,
    dataset_key TEXT NOT NULL,
    source_path TEXT NOT NULL,
    source_record_index INTEGER NOT NULL,
    FOREIGN KEY(poem_id) REFERENCES poems_zh_hans(id)
);
CREATE TABLE poems_fts_zh_hans (title TEXT, content_text TEXT);

CREATE TABLE dynasties_zh_hant (id INTEGER PRIMARY KEY, name TEXT NOT NULL, name_en TEXT, start_year INTEGER, end_year INTEGER);
CREATE TABLE authors_zh_hant (id INTEGER PRIMARY KEY, canonical_id TEXT NOT NULL UNIQUE, name TEXT NOT NULL, dynasty_id INTEGER NOT NULL, description TEXT);
CREATE TABLE poetry_types_zh_hant (id INTEGER PRIMARY KEY, name TEXT NOT NULL, category TEXT NOT NULL, lines INTEGER, chars_per_line INTEGER, description TEXT);
CREATE TABLE poems_zh_hant (
    id INTEGER PRIMARY KEY,
    canonical_id TEXT NOT NULL UNIQUE,
    canonical_fingerprint TEXT NOT NULL,
    dynasty_id INTEGER,
    author_id INTEGER,
    type_id INTEGER,
    title TEXT NOT NULL,
    content TEXT NOT NULL
    ,content_hash TEXT NOT NULL
);
CREATE TABLE poem_sources_zh_hant (
    id INTEGER PRIMARY KEY,
    poem_id INTEGER NOT NULL,
    locator_id TEXT NOT NULL UNIQUE,
    source_id TEXT,
    dataset_key TEXT NOT NULL,
    source_path TEXT NOT NULL,
    source_record_index INTEGER NOT NULL,
    FOREIGN KEY(poem_id) REFERENCES poems_zh_hant(id)
);
CREATE TABLE poems_fts_zh_hant (title TEXT, content_text TEXT);

INSERT INTO dynasties_zh_hans(id,name) VALUES (1, '唐');
INSERT INTO authors_zh_hans(id,canonical_id,name,dynasty_id) VALUES (1, 'cpa:author:v1:sha256:0000000000000001000000000000000000000000000000000000000000000000', '李白', 1);
INSERT INTO poetry_types_zh_hans(id,name,category) VALUES (1, '诗', '诗');
INSERT INTO poems_zh_hans(id,canonical_id,canonical_fingerprint,dynasty_id,author_id,type_id,title,content,content_hash) VALUES (1, 'cpa:poem:v2:sha256:0000000000000001000000000000000000000000000000000000000000000000', '00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000', 1, 1, 1, '静夜思', '["床前明月光"]', 'fixture-hans');
INSERT INTO poem_sources_zh_hans VALUES (1, 1, 'source:accepted:1', NULL, 'fixture', 'fixture/poems.json', 0);

INSERT INTO dynasties_zh_hant(id,name) VALUES (1, '唐');
INSERT INTO authors_zh_hant(id,canonical_id,name,dynasty_id) VALUES (1, 'cpa:author:v1:sha256:0000000000000001000000000000000000000000000000000000000000000000', '李白', 1);
INSERT INTO poetry_types_zh_hant(id,name,category) VALUES (1, '詩', '詩');
INSERT INTO poems_zh_hant(id,canonical_id,canonical_fingerprint,dynasty_id,author_id,type_id,title,content,content_hash) VALUES (1, 'cpa:poem:v2:sha256:0000000000000001000000000000000000000000000000000000000000000000', '00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000', 1, 1, 1, '靜夜思', '["牀前明月光"]', 'fixture-hant');
INSERT INTO poem_sources_zh_hant VALUES (1, 1, 'source:accepted:1', NULL, 'fixture', 'fixture/poems.json', 0);
SQL
}

make_release() {
    database=$1
    release_dir=$2
    mkdir -p "$release_dir"
	gzip -n -c "$database" >"${release_dir}/poetry.db.gz"
	database_digest=$(sha256_file "$database")
	archive_digest=$(sha256_file "${release_dir}/poetry.db.gz")
	{
		printf '%s  poetry.db\n' "$database_digest"
		printf '%s  poetry.db.gz\n' "$archive_digest"
	} >"${release_dir}/checksums.txt"
}

run_startup() {
    data_dir=$1
    release_dir=$2
    DATA_DIR=$data_dir \
    DATA_RELEASE_VERSION=v-test \
    DATA_RELEASE_BASE_URL="file://${release_dir}" \
    CURL_CONNECT_TIMEOUT=1 \
    CURL_MAX_TIME=3 \
    CURL_RETRIES=0 \
    STARTUP_VALIDATE_ONLY=true \
        sh "$startup"
}

contract_schema=$(jq -er '.pipeline.schema_version' "$contract")
[ "$contract_schema" = "2" ] || fail "contract schema is not v2"
contract_tables=$(jq -er '.quality_contract.required_tables | join(" ")' "$contract")
startup_tables=$(sed -n "s/^    required_tables='\(.*\)'$/\1/p" "$startup")
[ "$startup_tables" = "$contract_tables" ] || fail "startup required tables differ from the data contract"
grep -Fq "EXPECTED_SCHEMA_VERSION=\${EXPECTED_SCHEMA_VERSION:-2}" "$startup" ||
    fail "startup schema default differs from the data contract"
if DATA_DIR="${tmp_root}/latest" DATA_RELEASE_VERSION=latest STARTUP_VALIDATE_ONLY=true \
    sh "$startup" >/dev/null 2>&1; then
    fail "mutable latest data release was accepted"
fi

good_database=${tmp_root}/good.db
good_release=${tmp_root}/good-release
good_manifest=${tmp_root}/data-source-manifest.json
good_report=${tmp_root}/poetry.db.source-report.json
fixture_contract=${tmp_root}/source-manifest.json
create_fixture "$good_database" 2
make_release "$good_database" "$good_release"
cat >"$good_report" <<'JSON'
{
  "schema_version": 1,
  "files": [
    {
      "dataset_key": "fixture",
      "source_path": "fixture/excluded.json",
      "action": "excluded",
      "reason": "listed in dataset excludes",
      "total_records": 0,
      "accepted_records": 0,
      "rejected_records": 0
    },
    {
      "dataset_key": "fixture",
      "source_path": "fixture/poems.json",
      "action": "loaded",
      "total_records": 2,
      "accepted_records": 1,
      "rejected_records": 1
    }
  ],
  "totals": {
    "total_records": 2,
    "accepted_records": 1,
    "rejected_records": 1,
    "excluded_files": 1
  }
}
JSON
fixture_file_decisions_sha=$(jq -r '.files[] | [.dataset_key,.source_path,.action,(.reason//""),.total_records,.accepted_records,.rejected_records] | @tsv' "$good_report" |
    sha256_file /dev/stdin)
fixture_database_sha=$(sha256_file "$good_database")
fixture_poem_hans=$(sqlite_query_sha "$good_database" "SELECT canonical_id || '|' || canonical_fingerprint || '|' || id || '|' || ifnull(type_id,'') || '|' || ifnull(author_id,'') || '|' || ifnull(dynasty_id,'') || '|' || hex(title) || '|' || hex(content) || '|' || content_hash FROM poems_zh_hans ORDER BY canonical_id;")
fixture_poem_hant=$(sqlite_query_sha "$good_database" "SELECT canonical_id || '|' || canonical_fingerprint || '|' || id || '|' || ifnull(type_id,'') || '|' || ifnull(author_id,'') || '|' || ifnull(dynasty_id,'') || '|' || hex(title) || '|' || hex(content) || '|' || content_hash FROM poems_zh_hant ORDER BY canonical_id;")
fixture_author_hans=$(sqlite_query_sha "$good_database" "SELECT canonical_id || '|' || id || '|' || dynasty_id || '|' || hex(name) || '|' || hex(ifnull(description,'')) FROM authors_zh_hans ORDER BY canonical_id;")
fixture_author_hant=$(sqlite_query_sha "$good_database" "SELECT canonical_id || '|' || id || '|' || dynasty_id || '|' || hex(name) || '|' || hex(ifnull(description,'')) FROM authors_zh_hant ORDER BY canonical_id;")
fixture_witness_hans=$(sqlite_query_sha "$good_database" "SELECT s.locator_id || '|' || p.canonical_id || '|' || hex(ifnull(s.source_id,'')) || '|' || hex(s.dataset_key) || '|' || hex(s.source_path) || '|' || s.source_record_index FROM poem_sources_zh_hans s JOIN poems_zh_hans p ON p.id=s.poem_id ORDER BY s.locator_id;")
fixture_witness_hant=$(sqlite_query_sha "$good_database" "SELECT s.locator_id || '|' || p.canonical_id || '|' || hex(ifnull(s.source_id,'')) || '|' || hex(s.dataset_key) || '|' || hex(s.source_path) || '|' || s.source_record_index FROM poem_sources_zh_hant s JOIN poems_zh_hant p ON p.id=s.poem_id ORDER BY s.locator_id;")
fixture_taxonomy_hans=$(sqlite_query_sha "$good_database" "SELECT row FROM (SELECT 'D|' || id || '|' || hex(name) || '|' || hex(ifnull(name_en,'')) || '|' || ifnull(start_year,'') || '|' || ifnull(end_year,'') AS row, 1 AS kind, id AS sort_id FROM dynasties_zh_hans UNION ALL SELECT 'T|' || id || '|' || hex(name) || '|' || hex(category) || '|' || ifnull(lines,'') || '|' || ifnull(chars_per_line,'') || '|' || hex(ifnull(description,'')), 2, id FROM poetry_types_zh_hans) ORDER BY kind, sort_id;")
fixture_taxonomy_hant=$(sqlite_query_sha "$good_database" "SELECT row FROM (SELECT 'D|' || id || '|' || hex(name) || '|' || hex(ifnull(name_en,'')) || '|' || ifnull(start_year,'') || '|' || ifnull(end_year,'') AS row, 1 AS kind, id AS sort_id FROM dynasties_zh_hant UNION ALL SELECT 'T|' || id || '|' || hex(name) || '|' || hex(category) || '|' || ifnull(lines,'') || '|' || ifnull(chars_per_line,'') || '|' || hex(ifnull(description,'')), 2, id FROM poetry_types_zh_hant) ORDER BY kind, sort_id;")
fixture_rejections=$(sqlite_query_sha "$good_database" "SELECT locator_id || '|' || hex(ifnull(source_id,'')) || '|' || hex(dataset_key) || '|' || hex(source_path) || '|' || source_record_index || '|' || hex(stage) || '|' || hex(reason) FROM source_rejections ORDER BY locator_id;")
jq --arg file_decisions_sha "$fixture_file_decisions_sha" \
    --arg database_sha "$fixture_database_sha" \
    --arg poem_hans "$fixture_poem_hans" --arg poem_hant "$fixture_poem_hant" \
    --arg author_hans "$fixture_author_hans" --arg author_hant "$fixture_author_hant" \
    --arg witness_hans "$fixture_witness_hans" --arg witness_hant "$fixture_witness_hant" \
    --arg taxonomy_hans "$fixture_taxonomy_hans" --arg taxonomy_hant "$fixture_taxonomy_hant" \
    --arg rejections "$fixture_rejections" '
    .quality_contract.expected_source_totals =
      {total_records:2,accepted_records:1,rejected_records:1,excluded_files:1} |
    .quality_contract.expected_products =
      {poems_per_language:1,source_witnesses_per_language:1,authors_zh_hans:1,authors_zh_hant:1} |
    .quality_contract.min_authors_per_language = 1 |
    .quality_contract.expected_file_decisions = 2 |
    .quality_contract.expected_file_decisions_sha256 = $file_decisions_sha |
    .quality_contract.expected_database_sha256 = $database_sha |
    .quality_contract.expected_logical_digests = {
      poem_products_zh_hans:$poem_hans,poem_products_zh_hant:$poem_hant,
      author_products_zh_hans:$author_hans,author_products_zh_hant:$author_hant,
      source_witnesses_zh_hans:$witness_hans,source_witnesses_zh_hant:$witness_hant,
      taxonomy_zh_hans:$taxonomy_hans,taxonomy_zh_hant:$taxonomy_hant,
      source_rejections:$rejections} |
    .quality_contract.expected_rejections =
      [{count:1,reason:"fixture rejection",stage:"load"}] |
    .quality_contract.expected_poems_by_dynasty_zh_hans = {"唐":1} |
    .quality_contract.expected_categories_zh_hans = {"诗":1}
' "$contract" >"$fixture_contract"
source_repository=$(jq -er '.source.repository' "$contract")
source_commit=$(jq -er '.source.commit' "$contract")
source_license=$(jq -er '.source.license' "$contract")
pipeline_repository=$(jq -er '.pipeline.repository' "$contract")
pipeline_commit=$(git -C "$repo_root" rev-parse HEAD)
source_report_sha=$(sha256_file "$good_report")
jq -n \
    --arg source_repository "$source_repository" \
    --arg source_commit "$source_commit" \
    --arg source_license "$source_license" \
    --arg pipeline_repository "$pipeline_repository" \
    --arg pipeline_commit "$pipeline_commit" \
    --arg source_report_asset "$(basename -- "$good_report")" \
    --arg source_report_sha "$source_report_sha" \
    --arg database_sha "$fixture_database_sha" \
    '{schema_version:2, release_tag:"v0.0.0", generated_at:"2026-01-01T00:00:00Z",
      source:{repository:$source_repository,commit:$source_commit,license:$source_license},
      pipeline:{repository:$pipeline_repository,commit:$pipeline_commit},
      database:{asset:"poetry.db.gz",uncompressed_asset:"poetry.db",sha256:$database_sha},
      statistics:{poems_zh_hans:1,poems_zh_hant:1,authors_zh_hans:1,authors_zh_hant:1},
      source_report:{asset:$source_report_asset,sha256:$source_report_sha,schema_version:1,
        totals:{total_records:2,accepted_records:1,rejected_records:1,excluded_files:1}}}' \
    >"$good_manifest"
DATA_CONTRACT_PATH="$fixture_contract" sh "${script_dir}/verify-data-contract.sh" \
    --database "$good_database" --source-report "$good_report" \
    --manifest "$good_manifest" >/dev/null

# The release verifier must reject author identities that are structurally
# shaped like the prefix but contain non-hex digest bytes. Keeping the first
# 64 digest bits unchanged also proves the public-ID formula alone is not a
# substitute for validating the full canonical identity.
malformed_author_database=${tmp_root}/malformed-author.db
cp "$good_database" "$malformed_author_database"
sqlite3 "$malformed_author_database" \
    "UPDATE authors_zh_hans SET canonical_id='cpa:author:v1:sha256:0000000000000001zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz';
     UPDATE authors_zh_hant SET canonical_id='cpa:author:v1:sha256:0000000000000001zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz';"
if DATA_CONTRACT_PATH="$fixture_contract" sh "${script_dir}/verify-data-contract.sh" \
    --database "$malformed_author_database" --source-report "$good_report" \
    --manifest "$good_manifest" >"${tmp_root}/malformed-author.log" 2>&1; then
    fail "non-hex canonical author digest passed the data contract"
fi
malformed_author_release=${tmp_root}/malformed-author-release
malformed_author_install=${tmp_root}/malformed-author-install
make_release "$malformed_author_database" "$malformed_author_release"
if run_startup "$malformed_author_install" "$malformed_author_release" >"${tmp_root}/malformed-author-startup.log" 2>&1; then
    fail "non-hex canonical author digest passed startup"
fi
[ ! -e "${malformed_author_install}/poetry.db" ] || fail "malformed canonical author database was installed"
[ ! -e "${malformed_author_install}/checksums.txt" ] || fail "malformed canonical author release metadata was installed"

# Canonical author IDs are API-visible JavaScript-safe integers governed by the
# digest, not arbitrary row numbers. Update both language products together so
# only the formula check can detect the corruption.
wrong_author_id_database=${tmp_root}/wrong-author-id.db
cp "$good_database" "$wrong_author_id_database"
sqlite3 "$wrong_author_id_database" \
    "UPDATE authors_zh_hans SET id=2; UPDATE poems_zh_hans SET author_id=2;
     UPDATE authors_zh_hant SET id=2; UPDATE poems_zh_hant SET author_id=2;"
if DATA_CONTRACT_PATH="$fixture_contract" sh "${script_dir}/verify-data-contract.sh" \
    --database "$wrong_author_id_database" --source-report "$good_report" \
    --manifest "$good_manifest" >"${tmp_root}/wrong-author-id.log" 2>&1; then
    fail "author ID that disagrees with its canonical digest passed the data contract"
fi

# A valid digest-tail change preserves this fixture's truncated numeric ID but
# changes the full author entity. The Hans/Hant canonical sets must therefore
# be compared explicitly.
divergent_author_database=${tmp_root}/divergent-author.db
cp "$good_database" "$divergent_author_database"
sqlite3 "$divergent_author_database" \
    "UPDATE authors_zh_hant SET canonical_id='cpa:author:v1:sha256:0000000000000001000000000000000000000000000000000000000000000001';"
if DATA_CONTRACT_PATH="$fixture_contract" sh "${script_dir}/verify-data-contract.sh" \
    --database "$divergent_author_database" --source-report "$good_report" \
    --manifest "$good_manifest" >"${tmp_root}/divergent-author.log" 2>&1; then
    fail "divergent Hans/Hant canonical author sets passed the data contract"
fi

# SQL's != yields NULL rather than true for a missing relation. This regression
# keeps every author row intact and proves the cross-language poem relationship
# check uses NULL-safe comparison.
missing_author_relation_database=${tmp_root}/missing-author-relation.db
cp "$good_database" "$missing_author_relation_database"
sqlite3 "$missing_author_relation_database" "UPDATE poems_zh_hant SET author_id=NULL;"
if DATA_CONTRACT_PATH="$fixture_contract" sh "${script_dir}/verify-data-contract.sh" \
    --database "$missing_author_relation_database" --source-report "$good_report" \
    --manifest "$good_manifest" >"${tmp_root}/missing-author-relation.log" 2>&1; then
    fail "missing Hant author relationship passed the data contract"
fi

# The per-language relation gate must also reject an author row that does not
# exist, independently of the cross-language comparison.
orphan_author_relation_database=${tmp_root}/orphan-author-relation.db
cp "$good_database" "$orphan_author_relation_database"
sqlite3 "$orphan_author_relation_database" "UPDATE poems_zh_hans SET author_id=999; UPDATE poems_zh_hant SET author_id=999;"
if DATA_CONTRACT_PATH="$fixture_contract" sh "${script_dir}/verify-data-contract.sh" \
    --database "$orphan_author_relation_database" --source-report "$good_report" \
    --manifest "$good_manifest" >"${tmp_root}/orphan-author-relation.log" 2>&1; then
    fail "orphan poem author relationship passed the data contract"
fi

# A database that merely labels itself schema v2 must not pass without the
# canonical author column and its uniqueness contract. This is checked both by
# the release verifier and by startup's lightweight structural boundary.
legacy_author_database=${tmp_root}/legacy-author.db
cp "$good_database" "$legacy_author_database"
sqlite3 "$legacy_author_database" <<'SQL'
ALTER TABLE authors_zh_hans RENAME TO governed_authors_zh_hans;
CREATE TABLE authors_zh_hans (id INTEGER PRIMARY KEY, name TEXT NOT NULL, dynasty_id INTEGER NOT NULL);
INSERT INTO authors_zh_hans(id, name, dynasty_id)
    SELECT id, name, dynasty_id FROM governed_authors_zh_hans;
DROP TABLE governed_authors_zh_hans;
SQL
if DATA_CONTRACT_PATH="$fixture_contract" sh "${script_dir}/verify-data-contract.sh" \
    --database "$legacy_author_database" --source-report "$good_report" \
    --manifest "$good_manifest" >"${tmp_root}/legacy-author-contract.log" 2>&1; then
    fail "schema-v2 database without canonical author identity passed the data contract"
fi
legacy_author_release=${tmp_root}/legacy-author-release
legacy_author_install=${tmp_root}/legacy-author-install
make_release "$legacy_author_database" "$legacy_author_release"
if run_startup "$legacy_author_install" "$legacy_author_release" >"${tmp_root}/legacy-author-startup.log" 2>&1; then
    fail "schema-v2 database without canonical author identity passed startup"
fi
[ ! -e "${legacy_author_install}/poetry.db" ] || fail "legacy author schema was installed"
[ ! -e "${legacy_author_install}/checksums.txt" ] || fail "legacy author schema release metadata was installed"

# A fresh data directory installs a verified archive and its release checksum
# only after gzip, checksum, SQLite, schema, and table validation all succeed.
installed=${tmp_root}/installed
run_startup "$installed" "$good_release" >/dev/null || fail "fresh install did not succeed"
[ -s "${installed}/poetry.db" ] || fail "fresh install did not create the database"
[ -s "${installed}/checksums.txt" ] || fail "fresh install did not retain release metadata"
original_database_hash=$(sha256_file "${installed}/poetry.db")
original_manifest_hash=$(sha256_file "${installed}/checksums.txt")

# Once the local database is structurally valid and matches its retained
# release checksum, unavailable remote metadata must not turn a network outage
# into an API outage.
offline_log=${tmp_root}/offline.log
run_startup "$installed" "${tmp_root}/offline" >"$offline_log" 2>&1 ||
    fail "verified local fallback failed while offline"
grep -Fq 'continuing with verified local database' "$offline_log" ||
    fail "offline fallback was not reported"
if [ -e "${installed}/poetry.db-wal" ] || [ -e "${installed}/poetry.db-shm" ]; then
    fail "immutable local validation created SQLite WAL sidecars"
fi

# A downloadable archive with a false checksum must never replace the verified
# local database or its last-known-good checksum manifest.
bad_checksum_release=${tmp_root}/bad-checksum-release
mkdir -p "$bad_checksum_release"
cp "${good_release}/poetry.db.gz" "${bad_checksum_release}/poetry.db.gz"
{
	printf '%064d  poetry.db\n' 0
	printf '%064d  poetry.db.gz\n' 0
} >"${bad_checksum_release}/checksums.txt"
bad_checksum_log=${tmp_root}/bad-checksum.log
run_startup "$installed" "$bad_checksum_release" >"$bad_checksum_log" 2>&1 ||
    fail "bad remote checksum should fall back to a verified local database"
grep -Fq 'continuing with verified local database' "$bad_checksum_log" ||
    fail "bad checksum fallback was not reported"
[ "$(sha256_file "${installed}/poetry.db")" = "$original_database_hash" ] ||
    fail "bad checksum replaced the verified local database"
[ "$(sha256_file "${installed}/checksums.txt")" = "$original_manifest_hash" ] ||
    fail "bad checksum replaced the last-known-good release metadata"

# Exercise the second checksum boundary independently: the archive digest is
# correct, but the declared checksum of the uncompressed SQLite file is false.
# A fresh install must fail closed; an existing LKG pair must remain unchanged.
bad_database_checksum_release=${tmp_root}/bad-database-checksum-release
mkdir -p "$bad_database_checksum_release"
cp "${good_release}/poetry.db.gz" "${bad_database_checksum_release}/poetry.db.gz"
archive_hash=$(sha256_file "${bad_database_checksum_release}/poetry.db.gz")
{
	printf '%064d  poetry.db\n' 0
	printf '%s  poetry.db.gz\n' "$archive_hash"
} >"${bad_database_checksum_release}/checksums.txt"
bad_database_fresh=${tmp_root}/bad-database-fresh
if run_startup "$bad_database_fresh" "$bad_database_checksum_release" >"${tmp_root}/bad-database-fresh.log" 2>&1; then
	fail "false uncompressed database checksum succeeded on a fresh install"
fi
[ ! -e "${bad_database_fresh}/poetry.db" ] || fail "false database checksum left a fresh database"
[ ! -e "${bad_database_fresh}/checksums.txt" ] || fail "false database checksum left release metadata"
run_startup "$installed" "$bad_database_checksum_release" >"${tmp_root}/bad-database-lkg.log" 2>&1 ||
	fail "false uncompressed database checksum did not fall back to the LKG"
[ "$(sha256_file "${installed}/poetry.db")" = "$original_database_hash" ] ||
	fail "false database checksum replaced the LKG database"
[ "$(sha256_file "${installed}/checksums.txt")" = "$original_manifest_hash" ] ||
	fail "false database checksum replaced the LKG manifest"

# Simulate an untrappable interruption between the two asset renames. The
# fixed hidden backup pair must be detected and restored on the next startup.
interrupted_install=${tmp_root}/interrupted-install
mkdir -p "$interrupted_install"
cp "${installed}/poetry.db" "${interrupted_install}/.poetry.db.lkg"
cp "${installed}/checksums.txt" "${interrupted_install}/.checksums.txt.lkg"
: >"${interrupted_install}/.poetry.db.installing"
interrupted_database=${tmp_root}/interrupted-new.db
create_fixture "$interrupted_database" 1
cp "$interrupted_database" "${interrupted_install}/poetry.db"
cp "${installed}/checksums.txt" "${interrupted_install}/checksums.txt"
run_startup "$interrupted_install" "${tmp_root}/offline" >"${tmp_root}/recovery.log" 2>&1 ||
	fail "interrupted update did not recover its LKG pair"
grep -Fq 'Recovered the last verified database' "${tmp_root}/recovery.log" ||
	fail "interrupted update recovery was not reported"
[ "$(sha256_file "${interrupted_install}/poetry.db")" = "$original_database_hash" ] ||
	fail "interrupted update did not restore the LKG database"
if [ -e "${interrupted_install}/.poetry.db.lkg" ] ||
	[ -e "${interrupted_install}/.checksums.txt.lkg" ] ||
	[ -e "${interrupted_install}/.poetry.db.installing" ]; then
	fail "interrupted update left stale recovery backups"
fi

# If recovery itself is interrupted after only the database has been restored,
# the marker and unconsumed LKG pair must let a later startup finish safely.
interrupted_recovery=${tmp_root}/interrupted-recovery
mkdir -p "$interrupted_recovery"
cp "${installed}/poetry.db" "${interrupted_recovery}/.poetry.db.lkg"
cp "${installed}/checksums.txt" "${interrupted_recovery}/.checksums.txt.lkg"
: >"${interrupted_recovery}/.poetry.db.installing"
cp "${installed}/poetry.db" "${interrupted_recovery}/poetry.db"
printf '%064d  poetry.db\n%064d  poetry.db.gz\n' 0 0 >"${interrupted_recovery}/checksums.txt"
run_startup "$interrupted_recovery" "${tmp_root}/offline" >"${tmp_root}/recovery-second-phase.log" 2>&1 ||
	fail "partially restored startup pair was not recoverable"
[ "$(sha256_file "${interrupted_recovery}/poetry.db")" = "$original_database_hash" ] ||
	fail "partially restored startup did not recover the LKG database"
[ "$(sha256_file "${interrupted_recovery}/checksums.txt")" = "$original_manifest_hash" ] ||
	fail "partially restored startup did not recover the LKG manifest"

# A fresh install has no LKG. A crash between the two live renames leaves a
# marker and half-pair; the next process must discard it and retry cleanly.
fresh_interrupted=${tmp_root}/fresh-interrupted
mkdir -p "$fresh_interrupted"
: >"${fresh_interrupted}/.poetry.db.installing"
cp "$good_database" "${fresh_interrupted}/poetry.db"
run_startup "$fresh_interrupted" "$good_release" >"${tmp_root}/fresh-recovery.log" 2>&1 ||
	fail "fresh interrupted install was not cleaned and retried"
grep -Fq 'Discarded an interrupted fresh database installation' "${tmp_root}/fresh-recovery.log" ||
	fail "fresh interrupted install cleanup was not reported"
[ "$(sha256_file "${fresh_interrupted}/poetry.db")" = "$original_database_hash" ] ||
	fail "fresh interrupted install did not produce the verified database"

# A structurally valid but byte-modified database is not a verified release
# fallback. Switching journal mode changes the file while preserving its
# schema; an offline start must therefore fail closed. Immutable inspection
# must still avoid creating WAL/SHM sidecars.
tampered_install=${tmp_root}/tampered-install
mkdir -p "$tampered_install"
cp "${installed}/poetry.db" "${tampered_install}/poetry.db"
cp "${installed}/checksums.txt" "${tampered_install}/checksums.txt"
sqlite3 "${tampered_install}/poetry.db" 'PRAGMA journal_mode=WAL;' >/dev/null
rm -f -- "${tampered_install}/poetry.db-wal" "${tampered_install}/poetry.db-shm"
if run_startup "$tampered_install" "${tmp_root}/offline" >"${tmp_root}/tampered.log" 2>&1; then
	fail "byte-modified database was accepted as a verified offline release"
fi
grep -Fq 'not bound to its release checksum' "${tmp_root}/tampered.log" ||
	fail "checksum binding failure was not reported"
if [ -e "${tampered_install}/poetry.db-wal" ] || [ -e "${tampered_install}/poetry.db-shm" ]; then
	fail "immutable validation created SQLite WAL sidecars"
fi

# A schema-v1 archive in a fresh directory has no safe fallback and must fail
# closed without publishing a partial database.
old_database=${tmp_root}/old.db
old_release=${tmp_root}/old-release
old_install=${tmp_root}/old-install
create_fixture "$old_database" 1
make_release "$old_database" "$old_release"
if run_startup "$old_install" "$old_release" >"${tmp_root}/old-schema.log" 2>&1; then
    fail "incompatible schema succeeded without a local fallback"
fi
[ ! -e "${old_install}/poetry.db" ] || fail "incompatible schema was installed"
[ ! -e "${old_install}/checksums.txt" ] || fail "incompatible release metadata was installed"

echo "startup regression scenarios passed"
