### TESTING (category: "testing")

Code that tests application logic — unit tests, integration tests, test helpers.

## Shapes or common patterns

**assertion-quality** — Meaningful assertions vs tautological checks:
- Assertions that are always true regardless of implementation behavior (tautologies). Examples: asserting a variable against itself, checking that a constructed object is truthy, verifying a list has items without checking which items. The test passes whether the code works or is completely broken.
- Tests that only assert "no error" or "not nil" without verifying the actual returned value is correct
- Tests that check structural properties (length, type, key existence) but never check the actual data
- Assertions against hardcoded expected values that were copy-pasted from the first run's output rather than derived from requirements (the test documents what the code does, not what it should do)
- Missing negative assertions — test proves the happy path works but never verifies that invalid input is rejected, wrong state is prevented, or errors are raised when they should be
- Assertions undermined by the test setup — e.g., mocking the function being tested and then asserting on the mock's return value
- Assertions in the Arrange step instead of the Assert step. Tests should follow AAA / Given-When-Then: setup state (Arrange), exercise the unit (Act), then verify the outcome (Assert). Assertions on input or setup belong in the third step, against the output.

> Coverage gaps in **production** code (untested branches, missing regression tests, weakened assertions) belong to the `test-coverage` category. This file covers issues with the **test code itself** — assertions, correctness, reliability, structure.

**test-correctness** — Tests that pass but don't verify what they claim:
- Tests that mock the system under test (testing the mock, not the code)
- Setup that mutates shared state between tests (order-dependent results)
- Async tests without proper await/synchronization (race conditions in tests)
- Tests that pass because of coincidental data, not because logic is correct
- Copy-pasted tests with descriptions that don't match what they actually test
- Tests that swallow errors from the system under test (try/catch around the thing being tested)
- Assertions on stale variables (assert on input instead of output)

**test-reliability** — Flaky patterns, timing issues, environment dependence:
- Tests that depend on wall-clock time (`time.Now()`, `Date.now()`)
- Tests that depend on specific network availability or external services
- Sleep-based synchronization in async tests (fragile timing)
- Tests that depend on filesystem state, environment variables, or global config
- Non-deterministic test data (random values without seed control)
- Tests that pass in isolation but fail when run with other tests (shared state)
- Cleanup logic that doesn't run on test failure (resource leaks in test suite)

**structure-and-naming** — Test organization that aids diagnosis when failures land:
- Test names that don't convey the scenario or expected outcome (`TestFoo`, `TestSuccess`). Prefer a 3-part shape — unit-of-work / scenario / expected-result — e.g. `TestTransfer_InsufficientFunds_ReturnsError`, `it("rejects login when password expired")`.
- Long tests that interleave Arrange/Act/Assert without separation, making the failure cause unclear
- Loops over inputs without per-case names, so a failure says "test failed" rather than "case 3 (negative amount) failed". Prefer `t.Run(name, ...)` table-driven tests, `it.each`, or parametrized fixtures.
- Multiple unrelated behaviors asserted in the same test — split so each test fails for one reason
- Missed property-based or fuzz testing on parsers, validators, encoders, ID generators, and other input-shape-sensitive code where examples can't cover the space
- Helpers that hide what's being tested behind generic names (`setup()`, `assertOk()`) — the assertion or the surprising setup belongs visible in the test body

## Review criteria

[empty during migration — filled later via the Claude-Red coverage pass]
