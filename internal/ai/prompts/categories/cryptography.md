### CRYPTOGRAPHY (category: "cryptography")

Code that performs cryptographic operations or handles sensitive data.

## Shapes or common patterns

**encryption** — Algorithm choice, mode, padding, key size:
- Use of deprecated algorithms (DES, 3DES, RC4, Blowfish)
- ECB mode (patterns preserved in ciphertext)
- CBC without HMAC (padding oracle attacks)
- Missing or static initialization vectors (IV reuse)
- Insufficient key sizes (RSA < 2048, AES < 128)
- Custom encryption schemes instead of standard libraries
- Encryption without authentication (use AES-GCM or ChaCha20-Poly1305)

**key-management** — Storage, rotation, derivation, hardcoded keys:
- Hardcoded encryption keys, API keys, or secrets in source code
- Keys stored in plaintext in config files, databases, or environment variables without protection
- Missing key rotation mechanism or excessively long key lifetimes
- Key derivation without proper KDF (PBKDF2, scrypt, Argon2)
- Same key used for multiple purposes (encryption + signing)
- Keys logged or included in error messages

**hashing** — Password hashing, integrity verification, comparison:
- Passwords hashed with fast algorithms (MD5, SHA-1, SHA-256 without stretching)
- Missing salt or static/shared salt across users
- Bcrypt cost factor too low (< 10)
- Hash comparison using `==` instead of constant-time comparison
- Using hash for integrity without HMAC (length extension attacks)
- Truncated hashes that reduce collision resistance

**randomness** — PRNG vs CSPRNG, seed quality:
- `math/rand`, `Math.random()`, `random.random()` used for security purposes
- Predictable seeds (time-based, sequential, low entropy)
- Insufficient random bytes for tokens, IDs, or nonces
- PRNG state exposed or recoverable from output
- UUID v1/v4 misuse (v1 leaks MAC address and time; v4 may not be cryptographically random depending on implementation)

## Review criteria

[empty during migration — filled later via the Claude-Red coverage pass]
