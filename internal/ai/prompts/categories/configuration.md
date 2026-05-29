### CONFIGURATION (category: "configuration")

Code that handles secrets, environment configuration, defaults, and feature flags.

## Shapes or common patterns

**secrets-exposure** — Hardcoded credentials, logged secrets, leaked PII:
- Hardcoded API keys, passwords, tokens, or connection strings in source
- Secrets logged in application logs (tokens, passwords, PII, credit card numbers)
- Error messages that leak internal details (stack traces, file paths, version numbers)
- Secrets in comments, TODOs, or disabled code
- Credentials committed in configuration files
- Sensitive data included in HTTP responses or error payloads
- PII stored without encryption or access controls

**default-values** — Insecure defaults, missing validation:
- Debug mode enabled by default (should require explicit opt-in)
- Permissive CORS configuration as default (wildcard origins)
- Default credentials that work in production
- Security features disabled by default (CSRF protection, rate limiting)
- Default timeouts that are too long (or no timeout at all)
- Admin endpoints enabled by default without auth

**environment-handling** — Environment variable trust, fallback behavior:
- Environment variables used without validation or type checking
- Fallback to insecure defaults when env var is missing
- Production/development configuration switching based on untrusted input
- Secrets passed via command-line arguments (visible in process list)
- Configuration files loaded from user-writable locations
- Missing environment variable documentation (operators don't know what to set)

**dependency-security** — Dependencies, supply chain, security infrastructure:
- New dependencies added without review of what they do or their vulnerability history
- Lockfile not committed, out of sync with the manifest, or regenerated without intent (yarn.lock, package-lock.json, go.sum, Cargo.lock, poetry.lock, Pipfile.lock)
- Floating version specifiers (`*`, `latest`, `^1.x` with no lockfile) that allow silent transitive upgrades
- New dependency with a typo-squatted-looking name, very low download count, or recently published by an unknown author
- Postinstall / preinstall / build hooks introduced by a new dependency — these run arbitrary code on `npm install`
- Removal or downgrade of audit/SBOM tooling (`npm audit`, `pip-audit`, `govulncheck`, `cargo audit`, `osv-scanner`)
- Security headers modified or removed (CSP, HSTS, X-Frame-Options) — see also `web-security/security-headers`
- CORS configuration changes (especially wildcard origins in production) — see also `web-security/cors-config`
- Rate limiting removed or weakened
- Security middleware ordering changes (auth check moved after handler)
- Feature flags that gate security controls (disabling auth via config)

## Review criteria

[empty during migration — filled later via the Claude-Red coverage pass]
