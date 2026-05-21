### CROSS-CUTTING CONCERNS (category: "cross-cutting")

Issues that span multiple files or require understanding the change as a whole — incomplete refactors, inconsistent patterns, missing cascading updates.

#### Subcategories

**incomplete-refactors** — Changes that are only partially applied:
- Function/type renamed in definition but not in all callers
- Interface changed but not all implementations updated
- Data type changed but serialization/deserialization not updated
- Error type changed but error handling code still matches old type
- Moved functionality but left stale imports or references
- Database column renamed but queries or ORM mappings not updated

**inconsistent-patterns** — Same problem solved differently across files:
- Different error handling approaches in the same PR (some wrap, some don't)
- Mixed conventions for the same operation (some use helper, others inline)
- Inconsistent validation approaches (some validate at boundary, others deep inside)
- Different naming conventions within the same change
- Mixing async patterns (callbacks and promises, or channels and mutexes)

**missing-cascading-updates** — Changes that require corresponding updates elsewhere:
- Configuration changes without corresponding code changes
- Schema/migration changes without updating queries or ORM models
- API changes without updating client code, SDKs, or documentation
- Environment variable additions without updating deployment configs or docs
- New dependencies without updating build scripts, Docker images, or CI config
- Permission/role changes without updating authorization checks
