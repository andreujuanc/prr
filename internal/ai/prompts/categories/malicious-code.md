### MALICIOUS CODE (category: "malicious-code")

Code that's hostile by design, not by accident. The other categories ask "does this code have a bug?" — this one asks "is this code itself the attack?" Covers supply-chain compromises, insider-threat changes, test-time exploitation of contributors' machines, and code obfuscation. The shared trait is **intent**: the author knows what it does and is shipping it anyway.

Treat this category as adversarial review. Assume the contributor may not be acting in good faith. Read every suspicious construct twice. When evidence is ambiguous, surface the finding at lower severity rather than dismissing — a false positive costs a minute; a missed backdoor costs the project.

#### Subcategories

**install-time-execution** — Code that runs during `go install` / `npm install` / `make` / `pip install` / similar, executed before the user has read the source:
- `go:generate` directives that download and run arbitrary remote code
- `npm` `preinstall` / `postinstall` / `prepare` scripts new to the diff that fetch from network or execute shell
- Makefile install/build targets that pipe `curl` into `sh`/`bash`, or run unfamiliar binaries
- Python `setup.py` with `exec()` / `subprocess` of network-fetched content
- Dockerfile `RUN` steps that pull from non-pinned URLs (`curl https://... | sh`)
- `.cargo/config.toml`, `.npmrc`, `.pip.conf`, `.gradle/init.gradle` edits pointing at non-official registries
- `go.mod` `replace` directives pointing at suspicious forks or `github.com/<typo>` of known packages

**test-time-execution** — Tests, benchmarks, or fixtures that execute on the contributor's CI runner or another developer's machine and do something they shouldn't:
- Test code that makes outbound network calls (HTTP, DNS, SMTP) carrying environment, repo metadata, or credentials
- Test setup that writes to paths outside the test's own tmpdir — `~/.ssh/authorized_keys`, `~/.bashrc`, `~/.aws/credentials`, `/etc/`, system `PATH`
- `init()` functions in test files that do non-test work (network, exec, file system mutation)
- Test helpers that shell out to `curl`/`wget`/`bash -c` with arguments derived from env or remote responses
- Benchmarks that decode embedded payloads (base64, hex) into executable code or shell commands
- Test fixtures that `os.Setenv` secrets-handling variables (e.g., `AWS_ACCESS_KEY_ID`) and then leak them via logs / network

**backdoor-authentication** — Hidden auth bypass paths added under the cover of a legitimate-looking change:
- Hardcoded "magic" credentials (`if user == "admin_xyz" && pw == "..."`) that bypass normal authentication
- Undocumented HTTP header / query parameter checks that skip auth (`X-Internal-Override`, `?debug=1`)
- Role checks that special-case a specific user ID, email domain, or token prefix
- Token validators that return early on a recognizable malformed input (length, prefix, magic byte)
- Newly-added "debug" or "internal" endpoints with no authentication that expose privileged operations
- Constant-time comparison helpers replaced with `==` only on the auth path (allows timing oracle to be exploited)
- JWT verification weakened: accepts `alg: none`, accepts any signing key under specific conditions, skips signature check when a claim is present

**data-exfiltration** — Code paths that send sensitive data somewhere it doesn't belong:
- Outbound HTTP/DNS/SMTP calls carrying secrets, env vars (especially `*_TOKEN`, `*_KEY`, `*_SECRET`), file contents, or `os.Environ()`
- Logs that newly include credentials, session tokens, full request bodies, or PII (especially structured log fields)
- Error reporting that ships the panic / stack trace plus surrounding state to a third-party endpoint
- Telemetry payloads that include fields the telemetry contract doesn't allow (raw queries, user input, file paths)
- DNS lookups whose hostnames encode data (`<base64-encoded-secret>.attacker.com`) — classic DNS-tunnel exfil
- `git config` reads / `~/.netrc` reads / cloud-metadata-service calls (`169.254.169.254`) followed by outbound network

**persistence-and-host-tampering** — Changes that survive the immediate run by writing to the host:
- Writing files to user-home dotfiles (`~/.bashrc`, `~/.zshrc`, `~/.profile`, `~/.config/...` outside the project's own config)
- Modifying `~/.ssh/authorized_keys`, `~/.ssh/config`, `~/.aws/`, `~/.kube/`, `~/.docker/`
- Installing systemd units, launchd plists, cron entries, Windows services / scheduled tasks
- Mutating `/etc/hosts`, `/etc/resolv.conf`, system PATH, shell init scripts
- Browser-extension installation, IDE-plugin installation
- Self-replication: code that copies its own binary or installs itself outside the build's expected output paths

**obfuscation-and-hidden-control-flow** — Code shaped to evade casual review:
- Base64 / hex / gzip blobs that decode to shell commands or executable code
- Unicode bidi-override characters (`‮`, `⁦`–`⁩`) inside string literals or comments — *trojan source* attacks reorder what humans read vs what the compiler sees
- Zero-width characters in identifiers (`​`, `‌`, `‍`) — two functions can look identical but resolve differently
- Homoglyphs in identifiers (Cyrillic `а` vs Latin `a`) — same in display, different in code
- String concatenation / arithmetic that builds a command at runtime to defeat grep (`"cu" + "rl"`, `string([]byte{0x63,0x75,0x72,0x6c})`)
- `go:linkname` / `reflect`-based access to unexported runtime symbols with no documented reason
- Dead-looking branches gated on a specific date, environment variable, or git commit hash — *logic bombs*

**suspicious-dependencies** — Changes to declared dependencies that warrant a second look:
- New imports / `require` entries from typosquatted package names (`go-yaml` vs `goyaml`, `requests` vs `request`)
- Dependency upgrades to a release published in the last few days from an account with little history
- Dependency pinned to a commit SHA on an account that isn't the canonical maintainer's
- `replace` directives pointing at a fork that nobody on the team owns
- Lockfile changes (`go.sum`, `package-lock.json`, `Pipfile.lock`, `Cargo.lock`) for packages not touched by the PR's source changes
- Removed dependencies replaced with vendored copies whose contents differ subtly from upstream
- New transitive dep pulling in a package that doesn't belong (a logging library suddenly depending on a crypto-mining package)

**build-and-ci-tampering** — Pipeline changes whose effect is to compromise the build/release artifact:
- CI workflow edits that exfiltrate secrets via job output, artifact uploads, or third-party action calls
- New GitHub Actions / GitLab CI steps using unpinned action references (`uses: foo/bar@main`) on privileged jobs
- Release / publish steps newly running on PR triggers (where forks can poison the build)
- Container base images switched from a pinned digest to a tag, or from an official image to a third-party
- Build flags / linker flags newly enabling debug paths, disabling security mitigations, embedding remote URLs
- `.goreleaser`, `Dockerfile`, `Procfile` changes that insert an extra step running unrelated code at build / start
- Self-modifying build scripts that overwrite themselves based on remote content

#### Severity guidance

- **critical** — Active backdoor, install-time RCE, secret exfiltration confirmed by the code path. The change should not be merged.
- **high** — Strong indicators of intent: obfuscation in a security-sensitive path, hidden auth bypass behind a flag, host-tampering writes in code that has no business doing that. Demands a maintainer response before merge.
- **medium** — Suspicious shape but plausibly innocent: a new test that hits the network without an obvious reason, a dependency change that doesn't match the PR's stated scope. Calls for an explanation in the PR thread.
- **low** — Pattern-match only, no clear path to harm. Worth noting so future review can spot a trend.

When in doubt, prefer to **emit the finding and let the human decide** rather than dismiss. False positives here are cheap; false negatives are catastrophic.
