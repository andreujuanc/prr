### INPUT VALIDATION (category: "input-validation")

Code that receives, parses, or processes data from external sources.

#### Subcategories

**injection** — SQL, command, XSS, LDAP, header injection:
- String concatenation or interpolation in SQL queries
- Raw SQL with variable interpolation (`fmt.Sprintf` into query, f-strings, template literals)
- ORM methods that accept raw strings (`$queryRawUnsafe`, `whereRaw`, `Exec` with string args)
- NoSQL query construction with user-controlled keys/values (MongoDB operator injection)
- LDAP query construction with unescaped input
- `exec.Command`, `os/exec`, `subprocess`, `child_process` with user-influenced args
- Shell string construction (`sh -c`, backticks, `system()`) with variable input
- `eval()`, `Function()`, `compile()`, dynamic code execution
- `innerHTML`, `dangerouslySetInnerHTML`, `v-html` with user data
- Template rendering without auto-escaping
- HTTP response body written with `fmt.Fprintf` / `fmt.Sprintf` / `Write` / `ResponseWriter.Write` (or framework equivalents like Express `res.send`, Flask `Response`, Rails `render plain:`) that interpolates user-controlled strings into HTML, JS, or JSON-served-as-HTML without explicit escaping — server-rendered reflected XSS, where the user's input lands directly in the response body
- HTTP response headers set from user input (header injection, CRLF)
- Content-Type mismatches that enable type confusion

**path-traversal** — Directory traversal, file path manipulation:
- File read/write with user-controlled paths
- Path construction using concatenation or `Join` with external input
- Missing `../` sanitization or path canonicalization
- Temporary file creation with predictable names
- File upload handling that doesn't validate destination path
- Symlink following without checks
- Zip/archive extraction without path validation (zip slip)

**deserialization** — Untrusted data deserialization:
- JSON/XML/YAML parsing of external input without schema validation
- Protocol buffer deserialization from untrusted sources
- Pickle, Marshal, ObjectInputStream, `yaml.load` (unsafe loaders)
- Custom binary protocol parsing without bounds checking
- Regex with user-controlled patterns (ReDoS)
- XML parsing with external entity processing enabled (XXE)

**boundary-validation** — Validation at trust boundaries:
- Input from external sources (HTTP, files, env vars, CLI args, message queues) used without validation
- Validation happening deep inside the call chain instead of at the boundary where untrusted data enters
- Different validation rules applied at different layers for the same input (inconsistent enforcement)
- User-controlled URLs accepted without scheme/host validation (open redirects, SSRF)

**boundary-checks** — Overflow, underflow, length, type coercion:
- Integer overflow or underflow in arithmetic (especially sizes, counts, offsets)
- Missing length checks on strings, arrays, or buffers before use
- Signed/unsigned integer confusion
- Type coercion between numeric types that loses precision
- Array/slice index without bounds checking
- Negative numbers where only positive expected
- Unicode normalization issues (different byte representations of same character)
