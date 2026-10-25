# Nori

Nori is a personal assistant you text. This repository holds the whole product:

- [bot-core](bot-core/README.md): the Go agent service and its infrastructure.
- [bot-web](bot-web/README.md): the Next.js desktop web interface.

## Run

```sh
make dev     # bot-web on http://127.0.0.1:3210, bot-core on :8080
make check   # Go vet and gofmt, then TypeScript and ESLint
```

Install bot-web's dependencies first with `cd bot-web && bun install`. Each app reads its keys from its own `.env`; see the `.env.example` next to it.
