### TESTING (category: "testing")

Code that tests application logic — unit tests, integration tests, test helpers.

#### Subcategories

**assertion-quality** — Meaningful assertions vs tautological checks:
- Assertions that are always true regardless of implementation behavior (tautologies). Examples: asserting a variable against itself, checking that a constructed object is truthy, verifying a list has items without checking which items. The test passes whether the code works or is completely broken.
- Tests that only assert "no error" or "not nil" without verifying the actual returned value is correct
- Tests that check structural properties (length, type, key existence) but never check the actual data
- Assertions against hardcoded expected values that were copy-pasted from the first run's output rather than derived from requirements (the test documents what the code does, not what it should do)
- Missing negative assertions — test proves the happy path works but never verifies that invalid input is rejected, wrong state is prevented, or errors are raised when they should be
- Assertions undermined by the test setup — e.g., mocking the function being tested and then asserting on the mock's return value

> Coverage gaps in **production** code (untested branches, missing regression tests, weakened assertions) belong to the `test-coverage` category. This file covers issues with the **test code itself** — assertions, correctness, reliability.

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
