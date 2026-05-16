# Changelog

## Unreleased

- Added `mmb contact support` for first-class technical support questions from
  the CLI. It routes through the authenticated support intake path, emits JSON,
  supports stdin or explicit text, and honors shared mutation safety flags.
- Added `mmb contact product-feedback` as the clearer product-feedback command,
  while keeping `mmb agent-product-feedback` as a deprecated compatibility
  alias. This separates product feedback from local `mmb feedback` CLI notes
  without logging or documenting secrets.
