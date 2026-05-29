### TEST COVERAGE (category: "test-coverage")

Whether production code has adequate test coverage — flagged when reviewing non-test files.

## Shapes or common patterns

**coverage-gaps** — Missing test cases, untested paths:
- Happy path tested but error/edge cases missing
- Error handling code with no test that triggers the error
- Boundary conditions not tested (empty input, nil, zero, max values)
- Concurrency-sensitive code tested only sequentially
- Security-critical code paths without dedicated tests (auth bypass, injection)
- New public API methods without corresponding test cases
- Conditional branches where only one branch is exercised
- Bug fixes without a regression test that would catch the bug recurring
- Test assertions weakened to make tests pass (e.g., `assertEqual` relaxed to `assertNotNil`)

## Review criteria

[empty during migration — filled later via the Claude-Red coverage pass]
