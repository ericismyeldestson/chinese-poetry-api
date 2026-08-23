#!/bin/sh

set -eu

workflow=${1:-.github/workflows/test.yml}
validator=${2:-scripts/test-ci-workflow-policy.sh}
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/ci-workflow-policy.XXXXXX")
trap 'rm -rf -- "$work_dir"' EXIT HUP INT TERM

fail() {
  echo "CI workflow policy regression failure: $*" >&2
  exit 1
}

expect_rejected() {
  name=$1
  fixture=$2
  if cmp -s "$workflow" "$fixture"; then
    fail "$name fixture did not mutate the workflow"
  fi
  if ! ruby -ryaml -e \
    'YAML.safe_load(File.read(ARGV.fetch(0)), permitted_classes: [], permitted_symbols: [], aliases: true)' \
    "$fixture"; then
    fail "$name fixture is not valid YAML"
  fi
  if sh "$validator" "$fixture" >/dev/null 2>&1; then
    fail "$name was accepted"
  fi
}

expect_accepted() {
  name=$1
  fixture=$2
  if ! ruby -ryaml -e \
    'YAML.safe_load(File.read(ARGV.fetch(0)), permitted_classes: [], permitted_symbols: [], aliases: true)' \
    "$fixture"; then
    fail "$name fixture is not valid YAML"
  fi
  if ! sh "$validator" "$fixture" >/dev/null 2>&1; then
    fail "$name was rejected"
  fi
}

expect_mutation_accepted() {
  name=$1
  fixture=$2
  if cmp -s "$workflow" "$fixture"; then
    fail "$name fixture did not mutate the workflow"
  fi
  expect_accepted "$name" "$fixture"
}

expect_accepted "baseline workflow" "$workflow"

awk '
  $0 == "  test:" { in_test = 1 }
  in_test && $0 == "    runs-on: ubuntu-latest" {
    print
    print "    permissions: write-all"
    in_test = 0
    next
  }
  { print }
' "$workflow" >"$work_dir/test-write-all.yml"
expect_rejected "test job permissions: write-all" "$work_dir/test-write-all.yml"

awk '
  $0 == "  coverage-upload:" { in_coverage = 1 }
  in_coverage && $0 == "      id-token: write" {
    print
    print "      actions: write"
    in_coverage = 0
    next
  }
  { print }
' "$workflow" >"$work_dir/coverage-extra-write.yml"
expect_rejected "coverage job extra write permission" "$work_dir/coverage-extra-write.yml"

awk '
  $0 == "  coverage-upload:" { in_coverage = 1 }
  in_coverage && $0 ~ /uses: codecov\/codecov-action@/ {
    print
    print "        continue-on-error: true"
    in_coverage = 0
    next
  }
  { print }
' "$workflow" >"$work_dir/codecov-continue-on-error.yml"
expect_rejected "Codecov step continue-on-error" "$work_dir/codecov-continue-on-error.yml"

awk '
  $0 == "  coverage-upload:" { in_coverage = 1 }
  in_coverage && $0 == "    runs-on: ubuntu-latest" {
    print
    print "    continue-on-error: true"
    in_coverage = 0
    next
  }
  { print }
' "$workflow" >"$work_dir/coverage-continue-on-error.yml"
expect_rejected "coverage job continue-on-error" "$work_dir/coverage-continue-on-error.yml"

awk '
  $0 == "  full-data-preflight:" {
    print
    in_full_data = 1
    next
  }
  in_full_data && $0 == "    if: >-" {
    print "    if: ${{ always() }}"
    replacing_condition = 1
    next
  }
  replacing_condition && $0 == "    needs:" {
    replacing_condition = 0
    in_full_data = 0
    print
    next
  }
  replacing_condition { next }
  { print }
' "$workflow" >"$work_dir/full-data-always.yml"
expect_rejected "full-data always condition" "$work_dir/full-data-always.yml"

awk '
  $0 == "    branches: [main]" && !replaced {
    print "    branches: [\"**\"]"
    replaced = 1
    next
  }
  { print }
' "$workflow" >"$work_dir/all-push-branches.yml"
expect_rejected "unrestricted push branches" "$work_dir/all-push-branches.yml"

awk '
  $0 == "       github.event.pull_request.user.login == github.repository_owner &&" && !removed {
    removed = 1
    next
  }
  { print }
' "$workflow" >"$work_dir/non-owner-pr-author.yml"
expect_rejected "owner-triggered non-owner pull request" "$work_dir/non-owner-pr-author.yml"

awk '
  {
    sub(/codecov\/codecov-action@[0-9a-f]+/, "codecov/codecov-action@v7.0.0")
    print
  }
' "$workflow" >"$work_dir/codecov-unpinned.yml"
expect_rejected "unpinned Codecov action" "$work_dir/codecov-unpinned.yml"

awk '
  $0 == "  coverage-upload:" { in_coverage = 1 }
  in_coverage && $0 == "      - name: Upload coverage to Codecov with OIDC" {
    print
    print "        if: failure()"
    in_coverage = 0
    next
  }
  { print }
' "$workflow" >"$work_dir/codecov-step-if.yml"
expect_rejected "conditional Codecov upload step" "$work_dir/codecov-step-if.yml"

awk '
  $0 == "          use_oidc: true" {
    print "          use_oidc: false"
    next
  }
  { print }
' "$workflow" >"$work_dir/oidc-disabled.yml"
expect_rejected "disabled Codecov OIDC" "$work_dir/oidc-disabled.yml"

awk '
  $0 == "          fail_ci_if_error: true" {
    print "          fail_ci_if_error: false"
    next
  }
  { print }
' "$workflow" >"$work_dir/codecov-fail-open.yml"
expect_rejected "fail-open Codecov upload" "$work_dir/codecov-fail-open.yml"

awk '
  $0 == "jobs:" {
    print "env:"
    print "  CODECOV_TOKEN: $" "{{ secrets.CODECOV_TOKEN }}"
    print ""
  }
  { print }
' "$workflow" >"$work_dir/workflow-codecov-token.yml"
expect_rejected "workflow-level CODECOV_TOKEN" "$work_dir/workflow-codecov-token.yml"

awk '
  $0 == "  coverage-upload:" { in_coverage = 1 }
  in_coverage && $0 == "    runs-on: ubuntu-latest" {
    print
    print "    env:"
    print "      CODECOV_TOKEN: $" "{{ secrets.CODECOV_TOKEN }}"
    in_coverage = 0
    next
  }
  { print }
' "$workflow" >"$work_dir/job-codecov-token.yml"
expect_rejected "coverage job CODECOV_TOKEN" "$work_dir/job-codecov-token.yml"

awk '
  $0 == "  vulnerability-scan:" {
    print "      - name: Duplicate Codecov casing"
    print "        uses: Codecov/codecov-action@fb8b3582c8e4def4969c97caa2f19720cb33a72f"
    print ""
  }
  { print }
' "$workflow" >"$work_dir/duplicate-codecov-casing.yml"
expect_rejected "case-insensitive duplicate Codecov action" "$work_dir/duplicate-codecov-casing.yml"

awk '
  $0 == "  coverage-upload:" { in_coverage = 1 }
  in_coverage && $0 == "    if: >-" {
    print "    if: >"
    in_coverage = 0
    next
  }
  { print }
' "$workflow" >"$work_dir/equivalent-folding.yml"
expect_mutation_accepted "equivalent folded condition" "$work_dir/equivalent-folding.yml"

awk '
  $0 == "  coverage-upload:" { in_coverage = 1 }
  in_coverage && $0 == "    needs:" {
    getline
    if ($0 != "      - test") {
      exit 2
    }
    print "    needs: test"
    in_coverage = 0
    next
  }
  { print }
' "$workflow" >"$work_dir/equivalent-needs.yml"
expect_mutation_accepted "equivalent scalar needs" "$work_dir/equivalent-needs.yml"

echo "CI workflow policy regression scenarios passed"
