### AUTHENTICATION (category: "authentication")

Code that handles identity verification and session lifecycle.

#### Subcategories

**login-flow** — Login/logout handlers, multi-factor authentication:
- Login handlers that don't rate-limit or lock after failed attempts
- Missing brute-force protection on login endpoints
- Authentication bypass through parameter manipulation
- Login flows that leak whether a username exists (timing or error message)
- MFA bypass or fallback-to-single-factor paths

**session-management** — Session creation, validation, expiry, invalidation:
- Session tokens generated with insufficient entropy
- Sessions not invalidated on logout or password change
- Session fixation (accepting pre-authentication session IDs)
- Missing session expiry or excessively long lifetimes
- Session data stored client-side without integrity protection
- Concurrent session handling (no limit or no forced logout)

**token-validation** — JWT, OAuth, API key verification:
- JWT signature not verified, or `alg: none` accepted
- Token expiry (`exp`) not checked or clock skew too generous
- Audience (`aud`) or issuer (`iss`) not validated
- Refresh token reuse not detected (token rotation missing)
- API keys compared without constant-time comparison
- OAuth state parameter missing or not validated (CSRF)
- Token scope not checked before granting access

**password-handling** — Hashing, reset flows, storage:
- Passwords hashed with weak algorithms (MD5, SHA1, plain bcrypt without cost tuning)
- Password reset tokens that don't expire or are predictable
- Password reset flows that leak valid email addresses
- Plaintext passwords logged or stored
- Missing password complexity enforcement at the right boundary
