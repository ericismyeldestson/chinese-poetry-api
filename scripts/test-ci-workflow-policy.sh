#!/bin/sh

set -eu

workflow=${1:-.github/workflows/test.yml}

ruby - "$workflow" <<'RUBY'
require "yaml"

def mapping(value, label, errors)
  return value if value.is_a?(Hash)

  errors << "#{label} must be a mapping"
  {}
end

def sequence(value)
  return value.map(&:to_s) if value.is_a?(Array)
  return [] if value.nil?

  [value.to_s]
end

def normalized_condition(value)
  text = value.to_s.strip
  opening = "$" + "{{"
  if text.start_with?(opening) && text.end_with?("}}")
    text = text[opening.length...-2].strip
  end
  text.split.join(" ")
end

def fail_closed?(value)
  value.nil? || value == false
end

def exposes_codecov_token?(environment)
  return false unless environment.is_a?(Hash)

  environment.keys.any? { |key| key.to_s.casecmp("CODECOV_TOKEN").zero? }
end

def full_sha_action?(uses)
  return true if uses.start_with?("./", "docker://")

  separator = uses.rindex("@")
  return false if separator.nil? || separator.zero?

  ref = uses[(separator + 1)..]
  hex = "0123456789abcdef"
  ref.length == 40 && ref.each_char.all? { |character| hex.include?(character) }
end

path = ARGV.fetch(0)
errors = []

begin
  workflow = YAML.safe_load(
    File.read(path),
    permitted_classes: [],
    permitted_symbols: [],
    aliases: true
  )
rescue StandardError => error
  abort "CI workflow policy violation: cannot parse #{path}: #{error.message}"
end

workflow = mapping(workflow, "workflow", errors)
triggers = mapping(workflow["on"] || workflow[true], "on", errors)
expected_triggers = %w[pull_request push]
unless triggers.keys.map(&:to_s).sort == expected_triggers
  errors << "workflow triggers must be exactly push and pull_request"
end

expected_triggers.each do |event|
  event_config = mapping(triggers[event], "on.#{event}", errors)
  unless sequence(event_config["branches"]) == ["main"]
    errors << "on.#{event}.branches must be exactly main"
  end
end

global_permissions = workflow["permissions"]
unless global_permissions == { "contents" => "read" }
  errors << "global permissions must be exactly contents: read"
end
if exposes_codecov_token?(workflow["env"])
  errors << "workflow env must not expose CODECOV_TOKEN"
end

jobs = mapping(workflow["jobs"], "jobs", errors)
test_job = mapping(jobs["test"], "jobs.test", errors)
coverage_job = mapping(jobs["coverage-upload"], "jobs.coverage-upload", errors)
full_data_job = mapping(jobs["full-data-preflight"], "jobs.full-data-preflight", errors)

effective_test_permissions =
  if test_job.key?("permissions")
    test_job["permissions"]
  else
    global_permissions
  end
unless effective_test_permissions == { "contents" => "read" }
  errors << "test permissions must remain exactly contents: read"
end

jobs.each do |job_name, value|
  next unless value.is_a?(Hash)
  next if job_name.to_s == "coverage-upload"

  permissions = value["permissions"]
  if permissions == "write-all" ||
     (permissions.is_a?(Hash) && permissions["id-token"] == "write")
    errors << "only coverage-upload may receive id-token: write"
  end
end

expected_coverage_permissions = {
  "contents" => "read",
  "id-token" => "write"
}
unless coverage_job["permissions"] == expected_coverage_permissions
  errors << "coverage-upload permissions must be exactly contents: read and id-token: write"
end
if exposes_codecov_token?(coverage_job["env"])
  errors << "coverage-upload env must not expose CODECOV_TOKEN"
end

unless fail_closed?(coverage_job["continue-on-error"])
  errors << "coverage-upload must not continue on error"
end

trusted_condition =
  "github.event_name == 'push' || " \
  "(github.event_name == 'pull_request' && " \
  "github.event.pull_request.head.repo.full_name == github.repository && " \
  "github.event.pull_request.user.login == github.repository_owner && " \
  "github.actor == github.repository_owner)"

unless normalized_condition(coverage_job["if"]) == trusted_condition
  errors << "coverage-upload must use the trusted owner/main condition"
end

unless sequence(coverage_job["needs"]) == ["test"]
  errors << "coverage-upload must depend only on test"
end

all_codecov_steps = []
coverage_steps = coverage_job["steps"]
unless coverage_steps.is_a?(Array)
  errors << "coverage-upload.steps must be a sequence"
  coverage_steps = []
end

jobs.each do |job_name, value|
  next unless value.is_a?(Hash)

  steps = value["steps"]
  next unless steps.is_a?(Array)

  steps.each_with_index do |step, index|
    next unless step.is_a?(Hash)

    uses = step["uses"]
    next unless uses.is_a?(String)

    unless full_sha_action?(uses)
      errors << "jobs.#{job_name}.steps[#{index}].uses must pin a full commit SHA"
    end
    if uses.downcase.start_with?("codecov/codecov-action@")
      all_codecov_steps << [job_name.to_s, index, step]
    end
  end
end

if all_codecov_steps.length != 1
  errors << "workflow must contain exactly one Codecov action step"
end

codecov_entry = all_codecov_steps.first
if codecov_entry
  codecov_job_name, codecov_index, codecov_step = codecov_entry
  unless codecov_job_name == "coverage-upload"
    errors << "Codecov action must run only in coverage-upload"
  end

  expected_action =
    "codecov/codecov-action@fb8b3582c8e4def4969c97caa2f19720cb33a72f"
  unless codecov_step["uses"] == expected_action
    errors << "Codecov action must use the audited v7.0.0 commit"
  end

  inputs = mapping(codecov_step["with"], "Codecov with", errors)
  errors << "Codecov must use OIDC" unless inputs["use_oidc"] == true
  errors << "Codecov upload failures must fail CI" unless inputs["fail_ci_if_error"] == true
  errors << "Codecov must upload coverage.out" unless inputs["files"] == "./coverage.out"
  errors << "Codecov must retain the unittests flag" unless inputs["flags"] == "unittests"
  errors << "Codecov step must not have an if condition" if codecov_step.key?("if")
  unless fail_closed?(codecov_step["continue-on-error"])
    errors << "Codecov step must not continue on error"
  end
  errors << "Codecov OIDC mode must not provide a token" if inputs.key?("token")

  if exposes_codecov_token?(codecov_step["env"])
    errors << "Codecov OIDC mode must not expose CODECOV_TOKEN"
  end

  coverage_generators = coverage_steps.each_index.select do |index|
    step = coverage_steps[index]
    next false unless step.is_a?(Hash)

    step["run"].to_s.split.join(" ").include?("-coverprofile=coverage.out")
  end
  unless coverage_generators.length == 1
    errors << "coverage-upload must generate coverage.out exactly once"
  end
  if coverage_generators.first && codecov_job_name == "coverage-upload" &&
     coverage_generators.first >= codecov_index
    errors << "coverage.out must be generated before the Codecov step"
  end
end

unless fail_closed?(full_data_job["continue-on-error"])
  errors << "full-data-preflight must not continue on error"
end
unless normalized_condition(full_data_job["if"]) == trusted_condition
  errors << "full-data-preflight must use the trusted owner/main condition"
end
unless sequence(full_data_job["needs"]).include?("coverage-upload")
  errors << "full-data-preflight must depend on coverage-upload"
end

unless errors.empty?
  abort "CI workflow policy violations:\n- #{errors.join("\n- ")}"
end

puts "CI workflow security policy verified"
RUBY
