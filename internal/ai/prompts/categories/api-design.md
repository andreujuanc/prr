### API DESIGN (category: "api-design")

Code that defines, implements, or consumes API contracts.

## Shapes or common patterns

**contract-violations** — Breaking changes, missing validation, type mismatches:
- Renamed or removed public functions, types, or fields without deprecation
- Changed function signatures that break existing callers
- Modified return types or error behavior without updating consumers
- Required fields added to existing API responses without versioning
- Request validation missing for new fields (accepts anything)
- Response format changes that break existing consumers
- Enum values added without default/unknown handling on consumer side
- Nullable fields returned where non-null was previously guaranteed
- Content-Type negotiation missing or incorrect
- API documentation/spec diverged from implementation

**versioning** — Backwards compatibility, deprecation, migration:
- Breaking changes on unversioned endpoints
- Existing callers broken by changes without migration path
- Deprecated endpoints without migration path or timeline
- Version negotiation that falls through to latest (breaking old clients)
- Missing sunset headers or deprecation warnings
- Database schema changes that break older API versions
- Removed fields without deprecation period

**response-handling** — Error formats, status codes, pagination:
- Inconsistent error response format across endpoints
- Wrong HTTP status codes (200 for errors, 500 for client errors)
- Missing pagination on list endpoints (unbounded result sets)
- Error responses that leak implementation details
- Missing or incorrect Content-Type headers
- Inconsistent null vs absent vs empty representations
- Rate limit responses without Retry-After header

## Review criteria

[empty during migration — filled later via the Claude-Red coverage pass]
