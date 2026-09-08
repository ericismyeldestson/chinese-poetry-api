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

ruby - "$workflow" "$validator" "$work_dir" <<'RUBY'
require "yaml"
require "open3"

path, validator, work_dir = ARGV
baseline = YAML.safe_load(File.read(path), permitted_classes: [],
                          permitted_symbols: [], aliases: true)
gate = ->(workflow) { workflow.fetch("jobs").fetch("required-checks") }
step = ->(workflow) { gate.call(workflow).fetch("steps").fetch(0) }
mutations = {
  "deleted gate" => ->(w) { w.fetch("jobs").delete("required-checks") },
  "renamed gate job" => ->(w) { w.fetch("jobs")["renamed-gate"] = w.fetch("jobs").delete("required-checks") },
  "renamed check context" => ->(w) { gate.call(w)["name"] = "checks-are-green" },
  "missing always condition" => ->(w) { gate.call(w).delete("if") },
  "skipped gate job" => ->(w) { gate.call(w)["if"] = false },
  "success-only gate job" => ->(w) { gate.call(w)["if"] = "success()" },
  "extra dependency" => ->(w) { gate.call(w).fetch("needs") << "ghcr-write-probe" },
  "duplicate dependency" => ->(w) { gate.call(w).fetch("needs") << "test" },
  "missing permissions" => ->(w) { gate.call(w).delete("permissions") },
  "contents permission" => ->(w) { gate.call(w)["permissions"] = { "contents" => "read" } },
  "OIDC permission" => ->(w) { gate.call(w)["permissions"] = { "id-token" => "write" } },
  "package write permission" => ->(w) { gate.call(w)["permissions"] = { "packages" => "write" } },
  "masked gate job failure" => ->(w) { gate.call(w)["continue-on-error"] = true },
  "matrix check contexts" => ->(w) { gate.call(w)["strategy"] = { "matrix" => { "shard" => [1, 2] } } },
  "untrusted runner" => ->(w) { gate.call(w)["runs-on"] = "self-hosted" },
  "job environment injection" => ->(w) { gate.call(w)["env"] = { "BASH_ENV" => "./pr-script.sh" } },
  "global environment injection" => ->(w) { w["env"] = { "BASH_ENV" => "./pr-script.sh" } },
  "checkout before gate" => ->(w) { gate.call(w).fetch("steps").unshift({ "uses" => "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1" }) },
  "missing gate step" => ->(w) { gate.call(w)["steps"] = [] },
  "skipped gate step" => ->(w) { step.call(w)["if"] = false },
  "masked gate step failure" => ->(w) { step.call(w)["continue-on-error"] = true },
  "shell substitution" => ->(w) { step.call(w)["shell"] = "sh" },
  "checkout replaces inline step" => ->(w) { step.call(w)["uses"] = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"; step.call(w).delete("run") },
  "static success input" => ->(w) { step.call(w)["env"]["NEEDS_JSON"] = '{"test":{"result":"success"}}' },
  "partial needs input" => ->(w) { step.call(w)["env"]["NEEDS_JSON"] = '${{ toJSON(needs.test) }}' },
  "extra step environment" => ->(w) { step.call(w)["env"]["BASH_ENV"] = "./pr-script.sh" },
  "unconditional success" => ->(w) { step.call(w)["run"] = "exit 0\n" },
  "external gate script" => ->(w) { step.call(w)["run"] = "sh scripts/required-checks.sh\n" },
  "failure masked in inline script" => ->(w) { step.call(w)["run"] = step.call(w).fetch("run").sub("exit 1", "exit 0") },
  "neutral jobs accepted" => ->(w) { step.call(w)["run"] = step.call(w).fetch("run").sub('.result == "success"', '.result == "success" or .result == "neutral"') },
  "JSON stream check removed" => ->(w) { step.call(w)["run"] = step.call(w).fetch("run").sub("--slurp", "") }
}

gate.call(baseline).fetch("needs").each do |name|
  mutations["missing dependency #{name}"] = ->(w) { gate.call(w).fetch("needs").delete(name) }
  mutations["masked upstream job #{name}"] = ->(w) { w.fetch("jobs").fetch(name)["continue-on-error"] = true }
end
%w[test-ci-workflow-policy.sh test-ci-workflow-policy-regressions.sh test-required-checks.sh].each do |script|
  hooks = ->(w) { w.fetch("jobs").fetch("data-quality-contract").fetch("steps") }
  selected = ->(w) { hooks.call(w).find { |entry| entry["run"] == "sh scripts/#{script}" } }
  mutations["deleted test hook #{script}"] = ->(w) { hooks.call(w).delete(selected.call(w)) }
  mutations["skipped test hook #{script}"] = ->(w) { selected.call(w)["if"] = false }
  mutations["masked test hook #{script}"] = ->(w) { selected.call(w)["continue-on-error"] = true }
end

mutations.each_with_index do |(name, mutate), index|
  candidate = Marshal.load(Marshal.dump(baseline))
  mutate.call(candidate)
  abort "gate policy fixture did not mutate workflow: #{name}" if candidate == baseline
  fixture = File.join(work_dir, "gate-mutation-#{index}.yml")
  File.write(fixture, YAML.dump(candidate))
  _stdout, stderr, status = Open3.capture3("sh", validator, fixture)
  abort "gate policy regression accepted #{name}" if status.success?
  unless stderr.include?("CI workflow policy violation")
    abort "gate policy regression failed for an unrelated reason: #{name}: #{stderr}"
  end
end

accepted = {
  "explicit expression always" => ->(w) { gate.call(w)["if"] = '${{ always() }}' },
  "equivalent dependency order" => ->(w) { gate.call(w).fetch("needs").reverse! }
}
accepted.each_with_index do |(name, mutate), index|
  candidate = Marshal.load(Marshal.dump(baseline))
  mutate.call(candidate)
  abort "gate accepted fixture did not mutate workflow: #{name}" if candidate == baseline
  fixture = File.join(work_dir, "gate-accepted-#{index}.yml")
  File.write(fixture, YAML.dump(candidate))
  _stdout, stderr, status = Open3.capture3("sh", validator, fixture)
  abort "gate policy regression rejected #{name}: #{stderr}" unless status.success?
end
puts "required-checks policy regressions passed (#{mutations.length} rejected mutations, #{accepted.length} equivalent forms accepted)"
RUBY

echo "CI workflow policy regression scenarios passed"
