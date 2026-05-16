# useraccess

Small internal Go service that exposes a couple of HTTP endpoints
for looking up users. Backed by Postgres.

## Build

    go build ./...

## Layout

| File | Purpose |
|------|---------|
| `db.go` | database access |
| `handlers.go` | HTTP handlers |
| `util.go` | helpers |
