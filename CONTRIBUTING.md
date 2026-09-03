# Contributing

Thank you for helping improve devengine. Open an issue before substantial API,
schema, or lifecycle changes so the contract and compatibility impact can be
agreed first.

## Development

Use the Go version declared in `go.mod` and run the local quality gate:

```bash
go mod tidy -diff
test -z "$(gofmt -l .)"
go vet ./...
go test -race ./...
go test ./examples/...
```

PostgreSQL integration tests require `TEST_DATABASE_URL`; versions 17 and 18
are tested independently in CI. Tests should exercise public behavior and avoid
timing sleeps when explicit synchronization is possible.

Keep packages domain-neutral. Applications retain ownership of business
models, authorization, templates, and product policy. Document public API
changes in `CHANGELOG.md`.

Pull requests should explain the problem, contract, compatibility impact, and
verification performed. Participation is subject to the code of conduct.
