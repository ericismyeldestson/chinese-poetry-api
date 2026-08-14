#!/bin/sh
set -eu

DATA_DIR=${DATA_DIR:-data}
DB_FILE=${DB_FILE:-poetry.db}
DB_PATH=${DATA_DIR}/${DB_FILE}
CHECKSUM_FILE=${DATA_DIR}/checksums.txt
DATA_ARCHIVE_NAME=${DATA_ARCHIVE_NAME:-${DB_FILE}.gz}
DATA_RELEASE_VERSION=${DATA_RELEASE_VERSION:-v1.1.0}
EXPECTED_SCHEMA_VERSION=${EXPECTED_SCHEMA_VERSION:-2}
EXPECTED_CANONICAL_AUTHOR_ID_PREFIX=${EXPECTED_CANONICAL_AUTHOR_ID_PREFIX:-cpa:author:v1:sha256:}
DB_INTEGRITY_MODE=${DB_INTEGRITY_MODE:-quick_check}
CURL_CONNECT_TIMEOUT=${CURL_CONNECT_TIMEOUT:-10}
CURL_MAX_TIME=${CURL_MAX_TIME:-1800}
CURL_RETRIES=${CURL_RETRIES:-4}
CURL_RETRY_DELAY=${CURL_RETRY_DELAY:-3}
CURL_PROTOCOLS=${CURL_PROTOCOLS:-=https,file}
CURL_REDIRECT_PROTOCOLS=${CURL_REDIRECT_PROTOCOLS:-=https,file}

case "$DB_FILE" in
    ''|*/*) printf 'ERROR: DB_FILE must be a single file name\n' >&2; exit 1 ;;
esac
case "$DATA_ARCHIVE_NAME" in
    ''|*/*) printf 'ERROR: DATA_ARCHIVE_NAME must be a single file name\n' >&2; exit 1 ;;
esac

if [ "$DATA_RELEASE_VERSION" = "latest" ]; then
    printf 'ERROR: mutable DATA_RELEASE_VERSION=latest is not supported\n' >&2
    exit 1
fi
default_release_base="https://github.com/ericismyeldestson/chinese-poetry-api/releases/download/${DATA_RELEASE_VERSION}"

DATA_RELEASE_BASE_URL=${DATA_RELEASE_BASE_URL:-$default_release_base}
DATA_ARCHIVE_URL=${DATA_ARCHIVE_URL:-${DATA_RELEASE_BASE_URL}/${DATA_ARCHIVE_NAME}}
DATA_CHECKSUM_URL=${DATA_CHECKSUM_URL:-${DATA_RELEASE_BASE_URL}/checksums.txt}

checksum_tmp=
archive_tmp=
database_tmp=
database_backup=${DATA_DIR}/.${DB_FILE}.lkg
checksum_backup=${DATA_DIR}/.checksums.txt.lkg
install_marker=${DATA_DIR}/.${DB_FILE}.installing

cleanup() {
    for path in "$checksum_tmp" "$archive_tmp" "$database_tmp"; do
        if [ -n "$path" ] && [ -f "$path" ]; then
            rm -f -- "$path" || true
        fi
    done
}
trap cleanup EXIT HUP INT TERM

log() { printf '%s\n' "$*"; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
fail() { printf 'ERROR: %s\n' "$*" >&2; return 1; }

sqlite_uri() {
    path=$1
    case "$path" in
        /*) absolute=$path ;;
        *) absolute=$PWD/$path ;;
    esac
    # SQLite file URIs must percent-encode the path characters that otherwise
    # delimit a URI/query. Space is also encoded for portable CLI parsing.
    encoded=$(printf '%s' "$absolute" | sed \
        -e 's/%/%25/g' -e 's/ /%20/g' -e 's/?/%3F/g' -e 's/#/%23/g')
    printf 'file:%s?mode=ro&immutable=1\n' "$encoded"
}

sqlite_read() {
    candidate=$1
    query=$2
    sqlite3 "$(sqlite_uri "$candidate")" "$query"
}

download_to() {
    url=$1
    destination=$2
    curl --fail --location --silent --show-error \
        --proto "$CURL_PROTOCOLS" \
        --proto-redir "$CURL_REDIRECT_PROTOCOLS" \
        --connect-timeout "$CURL_CONNECT_TIMEOUT" \
        --max-time "$CURL_MAX_TIME" \
        --retry "$CURL_RETRIES" \
        --retry-delay "$CURL_RETRY_DELAY" \
        --retry-all-errors \
        --output "$destination" "$url"
}

checksum_for_name() {
    manifest=$1
	name=$2
	awk -v name="$name" '$2 == name || $2 == "*" name { print $1; exit }' "$manifest" |
        tr 'A-F' 'a-f' |
        grep -E '^[0-9a-f]{64}$'
}

checksum_for_archive() { checksum_for_name "$1" "$DATA_ARCHIVE_NAME"; }
checksum_for_database() { checksum_for_name "$1" "$DB_FILE"; }

sha256_file() {
	sha256sum "$1" | awk '{ print $1 }'
}

sync_data_dir() {
	# POSIX has no portable directory-fsync utility and Python is intentionally
	# not a runtime dependency. Flush pending filesystem writes globally, then
	# rely on the same-filesystem rename protocol plus a persistent marker/LKG
	# pair. Container storage drivers may still define their own power-loss
	# semantics.
	sync
}

copy_verified_file() {
	source=$1
	destination=$2
	temp=$(mktemp "${DATA_DIR}/.$(basename "$destination").restore.XXXXXX") || return 1
	if ! cp "$source" "$temp" || ! chmod 0600 "$temp" || ! sync "$temp" || ! mv -f "$temp" "$destination"; then
		rm -f -- "$temp"
		return 1
	fi
}

restore_lkg_pair() {
	[ -f "$database_backup" ] && [ -f "$checksum_backup" ] || return 1
	validate_database "$database_backup" || return 1
	database_matches_manifest "$database_backup" "$checksum_backup" || return 1
	copy_verified_file "$database_backup" "$DB_PATH" || return 1
	copy_verified_file "$checksum_backup" "$CHECKSUM_FILE" || return 1
	validate_database "$DB_PATH" || return 1
	database_matches_manifest "$DB_PATH" "$CHECKSUM_FILE" || return 1
	rm -f -- "$install_marker"
	sync_data_dir
	rm -f -- "$database_backup" "$checksum_backup"
}

database_matches_manifest() {
	candidate=$1
	manifest=$2
	[ -s "$candidate" ] && [ -s "$manifest" ] || return 1
	expected_bound=$(checksum_for_database "$manifest") || return 1
	actual_bound=$(sha256_file "$candidate") || return 1
	[ "$actual_bound" = "$expected_bound" ]
}

validate_database() {
    candidate=$1
    [ -s "$candidate" ] || fail "database is missing or empty: $candidate" || return 1

    case "$DB_INTEGRITY_MODE" in
        quick_check|integrity_check) ;;
        *) fail "unsupported DB_INTEGRITY_MODE: $DB_INTEGRITY_MODE"; return 1 ;;
    esac

    check_result=$(sqlite_read "$candidate" "PRAGMA ${DB_INTEGRITY_MODE};") || return 1
    [ "$check_result" = "ok" ] || fail "SQLite ${DB_INTEGRITY_MODE} failed: $check_result" || return 1

    schema_version=$(sqlite_read "$candidate" \
        "SELECT value FROM metadata WHERE key='schema_version' LIMIT 1;") || return 1
    [ "$schema_version" = "$EXPECTED_SCHEMA_VERSION" ] || {
        fail "schema version $schema_version is incompatible; expected $EXPECTED_SCHEMA_VERSION"
        return 1
    }

    required_tables='metadata source_rejections dynasties_zh_hans authors_zh_hans poetry_types_zh_hans poems_zh_hans poem_sources_zh_hans poems_fts_zh_hans dynasties_zh_hant authors_zh_hant poetry_types_zh_hant poems_zh_hant poem_sources_zh_hant poems_fts_zh_hant'
    for table in $required_tables; do
        present=$(sqlite_read "$candidate" \
            "SELECT count(*) FROM sqlite_master WHERE name='$table' AND type IN ('table','view');") || return 1
        [ "$present" = "1" ] || { fail "required table is missing: $table"; return 1; }
    done

    for language in zh_hans zh_hant; do
        poems=$(sqlite_read "$candidate" "SELECT EXISTS(SELECT 1 FROM poems_${language} LIMIT 1);") || return 1
        authors=$(sqlite_read "$candidate" "SELECT EXISTS(SELECT 1 FROM authors_${language} LIMIT 1);") || return 1
        if [ "$poems" != "1" ] || [ "$authors" != "1" ]; then
            fail "database has no usable poetry data for $language"
            return 1
        fi
        author_identity_column=$(sqlite_read "$candidate" \
            "SELECT count(*) FROM pragma_table_info('authors_${language}') WHERE name='canonical_id' AND \"notnull\"=1;") || return 1
        [ "$author_identity_column" = "1" ] || {
            fail "authors_${language}.canonical_id is missing or nullable"
            return 1
        }
        author_identity_unique=$(sqlite_read "$candidate" \
            "SELECT count(*) FROM pragma_index_list('authors_${language}') il WHERE il.\"unique\"=1 AND (SELECT count(*) FROM pragma_index_info(il.name))=1 AND EXISTS (SELECT 1 FROM pragma_index_info(il.name) WHERE name='canonical_id');") || return 1
        [ "$author_identity_unique" = "1" ] || {
            fail "authors_${language}.canonical_id is not uniquely constrained"
            return 1
        }
        invalid_author_identity=$(sqlite_read "$candidate" \
            "SELECT count(*) FROM authors_${language} WHERE canonical_id NOT GLOB '${EXPECTED_CANONICAL_AUTHOR_ID_PREFIX}*' OR length(canonical_id) != length('${EXPECTED_CANONICAL_AUTHOR_ID_PREFIX}') + 64 OR substr(canonical_id, length('${EXPECTED_CANONICAL_AUTHOR_ID_PREFIX}') + 1) GLOB '*[^0-9a-f]*' OR id < 1 OR id > 9007199254740991;") || return 1
        [ "$invalid_author_identity" = "0" ] || {
            fail "authors_${language} contains malformed canonical identity or unsafe public ID"
            return 1
        }
    done
}

recover_interrupted_install() {
	if [ ! -e "$install_marker" ] && [ ! -e "$database_backup" ] && [ ! -e "$checksum_backup" ]; then
		return 0
	fi
	if [ ! -f "$install_marker" ]; then
		# Backups can exist before the marker is durably created. Since the live
		# pair was not yet touched in that phase, clean them only after verifying it.
		if validate_database "$DB_PATH" && database_matches_manifest "$DB_PATH" "$CHECKSUM_FILE"; then
			rm -f -- "$database_backup" "$checksum_backup"
			sync_data_dir
			return 0
		fi
		fail "stale startup backups exist without a verified live pair"
		return 1
	fi

	# If the newly installed pair is already complete, the crash happened after
	# commit and only stale backups remain.
	if validate_database "$DB_PATH" && database_matches_manifest "$DB_PATH" "$CHECKSUM_FILE"; then
		rm -f -- "$install_marker"
		sync_data_dir
		rm -f -- "$database_backup" "$checksum_backup"
		log "Completed recovery cleanup for an already verified database update"
		return 0
	fi

	if restore_lkg_pair; then
		log "Recovered the last verified database after an interrupted update"
		return 0
	fi
	if [ ! -e "$database_backup" ] && [ ! -e "$checksum_backup" ]; then
		# A fresh install has no previous pair. A marker with no LKG means the
		# process stopped after validation but before the two-file commit was
		# acknowledged, so discard the half-pair and retry the versioned download.
		rm -f -- "$DB_PATH" "$CHECKSUM_FILE" "$install_marker"
		sync_data_dir
		log "Discarded an interrupted fresh database installation"
		return 0
	fi

	fail "interrupted database update has no recoverable verified pair"
}

fetch_remote_checksum() {
    checksum_tmp=$(mktemp "${DATA_DIR}/.checksums.XXXXXX") || return 1
    download_to "$DATA_CHECKSUM_URL" "$checksum_tmp" || return 1
    checksum_for_archive "$checksum_tmp" >/dev/null || return 1
	checksum_for_database "$checksum_tmp" >/dev/null || return 1
}

stage_database() {
    manifest=$1
    expected=$(checksum_for_archive "$manifest") || {
        fail "checksum manifest has no valid entry for $DATA_ARCHIVE_NAME"
        return 1
    }
	expected_database=$(checksum_for_database "$manifest") || {
		fail "checksum manifest has no valid entry for $DB_FILE"
		return 1
	}

    archive_tmp=$(mktemp "${DATA_DIR}/.${DATA_ARCHIVE_NAME}.XXXXXX") || return 1
    database_tmp=$(mktemp "${DATA_DIR}/.${DB_FILE}.XXXXXX") || return 1

    log "Downloading versioned database asset: $DATA_ARCHIVE_URL"
    download_to "$DATA_ARCHIVE_URL" "$archive_tmp" || return 1
	actual=$(sha256_file "$archive_tmp") || return 1
    [ "$actual" = "$expected" ] || {
        fail "database checksum mismatch (expected $expected, got $actual)"
        return 1
    }
    gzip -t "$archive_tmp" || { fail "database archive failed gzip validation"; return 1; }
    gzip -dc "$archive_tmp" >"$database_tmp" || return 1
	actual_database=$(sha256_file "$database_tmp") || return 1
	[ "$actual_database" = "$expected_database" ] || {
		fail "database checksum mismatch (expected $expected_database, got $actual_database)"
		return 1
	}
    validate_database "$database_tmp" || return 1

    chmod 0600 "$database_tmp" "$manifest" || return 1
	if [ "$local_valid" = true ]; then
		rm -f -- "$database_backup" "$checksum_backup" "$install_marker"
		ln "$DB_PATH" "$database_backup" 2>/dev/null || cp "$DB_PATH" "$database_backup" || return 1
		if ! ln "$CHECKSUM_FILE" "$checksum_backup" 2>/dev/null &&
			! cp "$CHECKSUM_FILE" "$checksum_backup"; then
			rm -f -- "$database_backup"
			return 1
		fi
	fi
	: >"$install_marker" || return 1
	chmod 0600 "$install_marker" || return 1
	sync "$install_marker" || return 1
	sync_data_dir
	if ! mv -f "$database_tmp" "$DB_PATH"; then
		if ! restore_lkg_pair; then
			fail "database installation failed and its LKG recovery state was retained"
		fi
		return 1
	fi
    database_tmp=
	if ! mv -f "$manifest" "$CHECKSUM_FILE"; then
		if ! restore_lkg_pair; then
			fail "checksum installation failed and its LKG recovery state was retained"
		fi
		return 1
	fi
    checksum_tmp=
	if ! validate_database "$DB_PATH" || ! database_matches_manifest "$DB_PATH" "$CHECKSUM_FILE"; then
		if ! restore_lkg_pair; then
			fail "installed pair was invalid and its LKG recovery state was retained"
		fi
		fail "installed database and checksum manifest do not form a verified pair"
		return 1
	fi
	rm -f -- "$install_marker"
	sync_data_dir
	rm -f -- "$database_backup" "$checksum_backup"
	log "Database and checksum manifest installed as a verified recoverable pair at $DB_PATH"
}

log "=== Chinese Poetry API independent startup ==="
mkdir -p "$DATA_DIR"
recover_interrupted_install || exit 1

local_valid=false
if [ -f "$DB_PATH" ]; then
    if validate_database "$DB_PATH"; then
		if database_matches_manifest "$DB_PATH" "$CHECKSUM_FILE"; then
			local_valid=true
			log "Verified existing release database: $DB_PATH"
		else
			warn "Existing database is structurally valid but is not bound to its release checksum"
		fi
    else
        warn "Existing database failed validation; it will not be used as a fallback"
    fi
fi

if fetch_remote_checksum; then
	remote_database_checksum=$(checksum_for_database "$checksum_tmp")
	if [ "$local_valid" = true ] && [ "$(sha256_file "$DB_PATH")" = "$remote_database_checksum" ]; then
		chmod 0600 "$checksum_tmp"
		mv -f "$checksum_tmp" "$CHECKSUM_FILE"
		checksum_tmp=
		log "Release database checksum is unchanged; verified local database retained"
    elif ! stage_database "$checksum_tmp"; then
        if [ "$local_valid" = true ]; then
            warn "Remote database update failed validation; continuing with verified local database"
        else
            fail "no verified database is available"
            exit 1
        fi
    fi
else
    if [ "$local_valid" = true ]; then
        warn "Release metadata is unavailable; continuing with verified local database"
    else
        fail "release metadata is unavailable and no verified local database exists"
        exit 1
    fi
fi

if [ "${STARTUP_VALIDATE_ONLY:-false}" = "true" ]; then
    log "Startup validation completed"
    exit 0
fi

log "Starting API server"
exec ./server
