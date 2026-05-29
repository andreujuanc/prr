### DESIGN & ARCHITECTURE (category: "design")

Structural and organizational qualities of the code — how components relate, how responsibilities are divided, how the code fits into the existing codebase.

## Shapes or common patterns

**abstraction-level** — Over-engineering vs under-abstraction:
- Unnecessary interfaces with only one implementation (premature abstraction)
- Copy-pasted logic blocks that should be extracted into a shared function
- Layers of indirection that add complexity without flexibility (wrapper classes that just delegate)
- God objects/functions that do too many things (>100 lines, >5 responsibilities)
- Premature generalization: parameterized for cases that don't exist yet
- Under-abstraction: inline logic that duplicates existing utilities or stdlib functions

**responsibility-separation** — Single responsibility, layer boundaries:
- Business logic mixed into HTTP handlers, CLI commands, or UI components
- Presentation/formatting logic in the data layer
- Database queries scattered across business logic instead of a data access layer
- Configuration parsing mixed with runtime behavior
- Logging/telemetry interleaved with core logic instead of using middleware or decorators
- Error formatting (user-facing messages) mixed with error detection

**codebase-consistency** — Following existing project patterns:
- Introducing a new pattern where an established one exists (new error handling style, different naming convention, alternative library for the same purpose)
- Diverging from the project's established directory structure or module organization
- Using a different configuration approach than the rest of the codebase
- Inconsistent use of framework features (some handlers use middleware, others inline the same logic)

**coupling** — Dependencies between components:
- Tight coupling between packages/modules that should be independent
- Circular dependencies or import cycles
- Components that can't be tested in isolation because of hard dependencies
- Global state or singletons used as implicit communication channels
- Concrete types where interfaces would allow substitution (especially for testing)
- Deep knowledge of another module's internals (reaching through multiple layers)

**api-surface** — Public interface design:
- New public functions/types that could be unexported (smallest public API principle)
- Public API that exposes internal implementation details
- Inconsistent naming relative to adjacent functions in the same package
- Missing or misleading documentation on public APIs
- API that is surprising — behavior differs from what the name/signature suggests

## Review criteria

[empty during migration — filled later via the Claude-Red coverage pass]
