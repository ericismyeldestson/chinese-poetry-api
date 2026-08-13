# Contributing

Small, reviewable pull requests are preferred. Use a feature branch, explain
the root cause and compatibility impact, and include tests for behavior changes.

The project requires Go 1.25.12, CGO, and SQLite built with FTS5 support. Before
opening a pull request, run:

```bash
gofmt -w ./cmd ./internal
CGO_ENABLED=1 go test -tags sqlite_fts5 ./...
CGO_ENABLED=1 go test -race -tags sqlite_fts5 ./...
go vet -tags sqlite_fts5 ./...
sh scripts/verify-data-contract.sh --contract-only
sh scripts/test-startup.sh
```

完整数据变更还必须运行 processor，并将 `data/poetry.db` 与确定性的
`data/poetry.db.source-report.json` 一起交给 `verify-data-contract.sh` 校验。

Run `make graphql-gen` after changing the GraphQL schema and include the
generated diff. Do not commit generated SQLite databases, build outputs, test
caches, or unrelated formatting changes.

Data changes must preserve the original record identity and source locator,
update the provenance manifest when the source revision changes, and include a
quality regression test. Do not silently delete conflicting textual witnesses
or author attributions.

Contributions to the covered program are provided under GPL-3.0. Report
security vulnerabilities using SECURITY.md rather than a public issue.
