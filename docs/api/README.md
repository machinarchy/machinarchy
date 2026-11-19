# API reference

`swagger.json` and `swagger.yaml` describe the HTTP API (OpenAPI 2.0). They
are generated from the handler annotations; do not edit them by hand.

```sh
make api-docs   # regenerate both files (needs swag, see ../development/prerequisites.md)
```

- The server embeds `swagger.json` (`docs/embed.go`). When
  `MACHINARCHY_API_DOCS_ENABLED` is on, it serves it at `/openapi.json` and an
  interactive reference at `/scalar`.
- `TestOpenAPISpecCoversEveryRoute` fails when a registered route is missing
  from `swagger.json`, so regenerate it after changing a handler.
- For a guided tour of the API through the CLI, see
  [the CLI user guide](../cli/user-full-guide.md).
