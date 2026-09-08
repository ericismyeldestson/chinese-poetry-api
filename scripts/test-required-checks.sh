#!/bin/sh

set -eu

workflow=${1:-.github/workflows/test.yml}

ruby - "$workflow" <<'RUBY'
require "yaml"
require "json"
require "open3"

workflow = YAML.safe_load(File.read(ARGV.fetch(0)), permitted_classes: [],
                          permitted_symbols: [], aliases: true)
# Execute the actual inline production gate. No shell or jq implementation is
# copied here: changing the workflow immediately changes what these tests run.
gate = workflow.fetch("jobs").fetch("required-checks")
script = gate.fetch("steps").fetch(0).fetch("run")
abort "required-checks inline script must be nonempty" unless script.is_a?(String) && !script.empty?

required = %w[test coverage-upload vulnerability-scan data-quality-contract
              license-contract container-preflight full-data-preflight]
success = required.to_h { |name| [name, { "result" => "success", "outputs" => {} }] }
scenarios = []
scenarios << ["all seven jobs succeed", JSON.generate(success), true]
scenarios << ["job order does not matter", JSON.generate(success.to_a.reverse.to_h), true]
with_outputs = Marshal.load(Marshal.dump(success))
with_outputs.fetch("test")["outputs"] = { "report" => "untrusted string: $(exit 0)" }
scenarios << ["output strings remain data", JSON.generate(with_outputs), true]

required.each do |name|
  %w[skipped neutral failure cancelled pending].each do |result|
    payload = success.merge(name => { "result" => result, "outputs" => {} })
    scenarios << ["#{name} result #{result}", JSON.generate(payload), false]
  end
  scenarios << ["missing job #{name}", JSON.generate(success.reject { |key, _| key == name }), false]
  [nil, "success", [], {}, { "outputs" => {} }, { "result" => true },
   { "result" => ["success"] }, { "result" => "SUCCESS" }].each_with_index do |value, index|
    scenarios << ["#{name} malformed result #{index}", JSON.generate(success.merge(name => value)), false]
  end
end

scenarios << ["unexpected extra job", JSON.generate(success.merge("extra-job" => { "result" => "success" })), false]
scenarios << ["Dependabot/external trusted jobs skipped", JSON.generate(success.merge(
  "coverage-upload" => { "result" => "skipped" },
  "full-data-preflight" => { "result" => "skipped" }
)), false]
scenarios << ["test failed and downstream jobs skipped", JSON.generate(success.merge(
  "test" => { "result" => "failure" },
  "coverage-upload" => { "result" => "skipped" },
  "full-data-preflight" => { "result" => "skipped" }
)), false]
scenarios << ["all jobs skipped", JSON.generate(required.to_h { |name| [name, { "result" => "skipped" }] }), false]
scenarios << ["all jobs cancelled", JSON.generate(required.to_h { |name| [name, { "result" => "cancelled" }] }), false]
scenarios << ["two valid JSON documents", "#{JSON.generate(success)}\n#{JSON.generate(success)}", false]
scenarios << ["bad first JSON document then success", "{}\n#{JSON.generate(success)}", false]
scenarios << ["trailing invalid JSON", "#{JSON.generate(success)}\ninvalid", false]
["", " ", "{", "null", "false", "0", '"success"', "[]", "{}"].each_with_index do |payload, index|
  scenarios << ["invalid top-level payload #{index}", payload, false]
end

failures = []
scenarios.each do |name, payload, expected_success|
  _stdout, stderr, status = Open3.capture3(
    { "NEEDS_JSON" => payload }, "bash", "--noprofile", "--norc",
    "-e", "-o", "pipefail", "-c", script
  )
  if status.success? != expected_success
    failures << "#{name}: expected #{expected_success ? 'success' : 'failure'}, exit #{status.exitstatus}; #{stderr.strip}"
  end
end
abort "required-checks execution regressions failed:\n- #{failures.join("\n- ")}" unless failures.empty?
puts "required-checks execution regressions passed (#{scenarios.length} scenarios; actual workflow inline script)"
RUBY
