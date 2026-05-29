### AUTHORIZATION (category: "authorization")

Code that makes access control decisions after identity is established.

## Shapes or common patterns

**access-control** — RBAC, permissions, ACLs, policy enforcement:
- Missing permission checks on endpoints or operations
- Role checks that use string comparison instead of proper RBAC
- Inconsistent permission enforcement (checked in some paths, not others)
- Default-allow policies (access granted unless explicitly denied)
- Permission checks in client code only, not enforced server-side
- Middleware ordering that allows requests to bypass auth checks
- Hardcoded role names or permission strings scattered across codebase

**resource-ownership** — IDOR, tenant isolation, data boundaries:
- Direct object references without ownership verification (IDOR)
- User A can access User B's resources by changing an ID in the request
- Missing tenant isolation in multi-tenant systems (cross-tenant data leaks)
- Database queries that don't filter by the authenticated user/tenant
- File or resource access that relies on obscurity (unguessable URLs) instead of auth
- Bulk operations that don't verify ownership of each item

**privilege-escalation** — Elevation of access, role manipulation:
- Users can modify their own role or permissions
- Admin functionality accessible through undocumented endpoints
- API endpoints that accept a `role` or `is_admin` parameter from the client
- Service-to-service calls that implicitly trust the caller without verification
- Impersonation features without proper audit logging
- Vertical escalation (user → admin) or horizontal (user A → user B)

## Review criteria

[empty during migration — filled later via the Claude-Red coverage pass]
