### WEB SECURITY (category: "web-security")

Browser-facing concerns that don't fit cleanly into injection, authentication, or authorization — CSRF, security headers, cookie attributes, CORS, clickjacking, parameter binding.

## Shapes or common patterns

**csrf-protection** — Cross-Site Request Forgery defenses:
- State-changing endpoints (POST/PUT/DELETE/PATCH) without CSRF token validation
- CSRF tokens validated only on some endpoints — coverage gaps
- Tokens that don't rotate per session or aren't bound to the user
- SameSite cookie policy missing or set to `None` without `Secure`
- Custom-header CSRF defenses (X-Requested-With) without enforcing the header presence on the server
- Form actions that accept GET for state changes (cacheable + no CSRF)

**security-headers** — Response headers that mitigate browser attacks:
- Missing `Content-Security-Policy` or CSP that uses `unsafe-inline` / `unsafe-eval` without justification
- Missing `Strict-Transport-Security` (HSTS) on HTTPS responses, or short `max-age`
- Missing `X-Frame-Options: DENY` (or CSP `frame-ancestors`) on pages that shouldn't be embedded
- Missing `X-Content-Type-Options: nosniff`
- Missing or permissive `Referrer-Policy` leaking URLs to third parties
- Missing `Permissions-Policy` for sensitive features (camera, geolocation, etc.)
- Headers set per-handler instead of via shared middleware (drift risk)

**cors-config** — Cross-Origin Resource Sharing:
- `Access-Control-Allow-Origin: *` combined with credentialed requests (browser will block, but indicates intent confusion)
- Origin allowlist that reflects the request's `Origin` header without validation (effectively `*`)
- `Access-Control-Allow-Credentials: true` with a permissive origin
- Pre-flight handling missing for non-simple methods/headers
- CORS applied per-route inconsistently — some endpoints exposed cross-origin that shouldn't be

**clickjacking** — UI redress attacks:
- Sensitive UI (login, settings, transfer) framable due to missing `X-Frame-Options` / CSP `frame-ancestors`
- Confirmation dialogs that can be auto-clicked when framed
- OAuth/SSO consent screens framable

**cookie-attributes** — Cookie hardening:
- Session cookies without `Secure` flag (sent over HTTP)
- Session cookies without `HttpOnly` (accessible to JS — XSS escalates to session theft)
- Missing or `None` `SameSite` on session cookies (CSRF exposure)
- Session cookie scope too broad (`Path=/` for an admin-only cookie, wildcard `Domain`)
- Cookie expiry mismatched with session lifetime (long-lived cookies for short-lived sessions)

**parameter-binding** — Mass assignment, over-posting:
- Whole request body bound directly to a domain/DB model (`json.Unmarshal` into the persisted struct)
- No allowlist of writable fields on update endpoints (caller can set `is_admin`, `tenant_id`, `role`)
- ORM `Updates(struct)` or equivalent that writes whatever fields are set on the input
- DTO/model boundary not enforced — internal-only fields exposed through the API shape

**redirects-and-navigation** — Open redirects, host validation:
- User-controlled URL passed to `Location` header without scheme/host allowlist (cross-ref `input-validation/boundary-validation`)
- OAuth `redirect_uri` validated by substring match instead of exact host comparison
- `next`/`return_to`/`continue` query params followed without validation after login

## Review criteria

[empty during migration — filled later via the Claude-Red coverage pass]
