# Spec acquisition

How the `ws1` CLI obtains, identifies, and consumes the OpenAPI specs that drive its operation surface.

For the automated maintainer pipeline that ties this all together, see `docs/build-pipeline.md`.

## Background

WS1 UEM does **not** officially publish a single OpenAPI / Swagger document. Other Omnissa products (Identity Manager, Notifications Service) do; UEM does not.

However, every WS1 UEM tenant exposes per-section OpenAPI 3.0.1 documents at predictable URLs on its `/api/help` endpoint. The page renders a Swagger UI; the underlying spec for each section is fetchable directly.

## Discovery surface

The tenant's `/api/help` page renders a static HTML index listing every available API section. Confirmed shape (from `as1831.awmdm.com`, API Explorer 2025.11.6.68):

```html
<table>
  <tr><td><a href="/api/help/Docs/Explore?urls.primaryName=MAM%20API%20V1">MAM API V1</a></td><td>Workspace ONE UEM MAM REST API V1</td></tr>
  <tr><td><a href="/api/help/Docs/Explore?urls.primaryName=MAM%20API%20V2">MAM API V2</a></td><td>Workspace ONE UEM MAM REST API V2</td></tr>
  <tr><td><a href="/api/help/Docs/Explore?urls.primaryName=MCM%20API">MCM API</a></td><td>...</td></tr>
  <tr><td><a href="/api/help/Docs/Explore?urls.primaryName=MDM%20API%20V1">MDM API V1</a></td><td>...</td></tr>
  <tr><td><a href="/api/help/Docs/Explore?urls.primaryName=MDM%20API%20V2">MDM API V2</a></td><td>...</td></tr>
  <tr><td><a href="/api/help/Docs/Explore?urls.primaryName=MDM%20API%20V3">MDM API V3</a></td><td>...</td></tr>
  <tr><td><a href="/api/help/Docs/Explore?urls.primaryName=MDM%20API%20V4">MDM API V4</a></td><td>...</td></tr>
  <tr><td><a href="/api/help/Docs/Explore?urls.primaryName=MEM%20API">MEM API</a></td><td>...</td></tr>
  <tr><td><a href="/api/help/Docs/Explore?urls.primaryName=System%20API%20V1">System API V1</a></td><td>...</td></tr>
  <tr><td><a href="/api/help/Docs/Explore?urls.primaryName=System%20API%20V2">System API V2</a></td><td>...</td></tr>
</table>
<footer>Workspace ONE UEM API Explorer 2025.11.6.68 - <a>Terms</a></footer>
```

Two pieces of data we care about:

1. The `<a href>` table rows — one per section.
2. The footer's `API Explorer <version>` — our canonical tenant version stamp.

### Discovery procedure

1. GET `https://<tenant>/api/help` (no auth needed for the index page).
2. Parse with `golang.org/x/net/html` (not regex — the structure is HTML, treat it as such).
3. Extract `<a href="/api/help/Docs/Explore?urls.primaryName=...">DISPLAY_NAME</a>` rows.
4. For each, slugify the display name (see below).
5. Extract footer text matching `Workspace ONE UEM API Explorer <version>` — use as the tenant version stamp.

### Slugification rule

Display name → slug:

1. Lowercase.
2. Strip the literal substring `api` (case-insensitive after step 1).
3. Strip whitespace.
4. If the result ends with `v<digits>`, that's the slug. Otherwise append `v1`.

| Display name | Slug |
|---|---|
| MAM API V1 | `mamv1` |
| MAM API V2 | `mamv2` |
| MCM API | `mcmv1` |
| MDM API V1 | `mdmv1` |
| MDM API V2 | `mdmv2` |
| MDM API V3 | `mdmv3` |
| MDM API V4 | `mdmv4` |
| MEM API | `memv1` |
| System API V1 | `systemv1` |
| System API V2 | `systemv2` |

Spec URL for each slug: `https://<tenant>/api/help/Docs/<slug>` (returns OpenAPI 3.0.1 JSON; auth required).

## Confirmed sample shape

From `https://as1831.awmdm.com/api/help/Docs/mcmv1`:

```json
{
  "openapi": "3.0.1",
  "info": {
    "title": "MCM API",
    "description": "Workspace ONE UEM MCM REST API",
    "contact": { "name": "Workspace ONE UEM" },
    "version": "1"
  },
  "servers": [
    { "url": "https://as1831.awmdm.com/api/mcm" }
  ],
  "paths": {
    "/awcontents": {
      "get": {
        "tags": ["AwContents"],
        "summary": "New - Search managed content.",
        "description": "Search managed content for the specified parameters.",
        "operationId": "AwContents_SearchAsync",
        "parameters": [ ... ]
      }
    }
  }
}
```

Notable properties:

- The spec is OpenAPI **3.0.1** — fully supported by `oapi-codegen` for Go and most other code generators.
- The `servers[].url` is **tenant-specific**. Code-gen must rewrite this to a config-driven base; otherwise the generated client is hard-coded to one tenant.
- `tags[0]` is the natural object grouping (e.g. `AwContents`, `Devices`, `SmartGroups`).
- `operationId` follows the pattern `<Tag>_<Verb>Async` or `<Tag>_<Verb>`.

## Repo layout

```
spec/
├── VERSION              # tenant identity, API Explorer version, fetch timestamp, file shas
├── mamv1.json
├── mamv2.json
├── mcmv1.json
├── mdmv1.json
├── mdmv2.json
├── mdmv3.json
├── mdmv4.json
├── memv1.json
├── systemv1.json
└── systemv2.json
```

`spec/VERSION` shape (YAML):

```yaml
tenant: as1831.awmdm.com
api_explorer_version: 2025.11.6.68
fetched_at: 2026-04-30T14:00:00Z
sections:
  - slug: mamv1
    sha256: 3f5a...
  - slug: mdmv4
    sha256: 8c2d...
  # ... etc
```

Drift detection at runtime: CLI compares `spec/VERSION:api_explorer_version` (compiled-in) against the live tenant's footer (or System API report). Mismatch surfaces as `SPEC_VERSION_MISMATCH` with the version delta.

## Operation naming

Canonical op identifier (used in `operations.policy.yaml`, in the JSON envelope's `operation` field, and in CLI command paths):

```
<section-slug>.<tag-lowercase>.<verb-lowercase>
```

The section-slug **includes the version suffix** because some sections (notably MDM with v1–v4) ship concurrent versions with different signatures.

Derivation at code-gen time:

| Source | Example | Becomes |
|---|---|---|
| Spec file name | `mcmv1.json` | section slug: `mcmv1` |
| `tags[0]` | `AwContents` | tag: `awcontents` |
| `operationId` suffix | `AwContents_SearchAsync` | verb: `search` |

Result: `mcmv1.awcontents.search`.

Edge cases:

- **No `tags`:** fall back to deriving the tag from the path's first segment (e.g. `/awcontents` → `awcontents`).
- **Multiple tags:** use `tags[0]`; warn at code-gen time if `tags.length > 1` so the maintainer can confirm.
- **`operationId` without `_`:** lowercase the whole thing as the verb.
- **`Async` suffix:** strip it (it's a .NET implementation artifact, not semantically meaningful).
- **Verb collisions within a tag:** if two operations would generate the same op name, append the HTTP method (`get`, `post`) as a disambiguator and emit a code-gen warning. Should be rare.

CLI command path mirrors the op identifier, dot-to-space:

| Op identifier | CLI command |
|---|---|
| `mcmv1.awcontents.search` | `ws1 mcmv1 awcontents search` |
| `mdmv4.devices.lock` | `ws1 mdmv4 devices lock` |
| `systemv2.users.search` | `ws1 systemv2 users search` |

The version-in-command is verbose. v0.5+ adds shortcut sugar (`ws1 mdm devices lock --api v4` or a `latest` alias). v0 keeps it explicit and unambiguous.

## Fallback paths

If `/api/help/Docs/<slug>` is ever unreachable (older WS1 build, custom deployment, network restriction):

1. **Postman collection export.** Omnissa publishes a Postman collection at <https://www.postman.com/workspace-one-uem/workspace-one-uem-apis/>. Postman exports collections to OpenAPI 3.0 (Collection → ⋯ → Export → OpenAPI 3.0). Resulting file is one document covering all sections; needs splitting by tag prefix.

2. **`ancalabrese/ws1cli` checked-in `spec.json`.** Useful as a structural reference. License caveat (GPL/Apache mismatch in the repo). Do not adopt verbatim.

## When to re-pull

- WS1 tenant version bumps (visible in `/api/help` footer; Omnissa ships ~quarterly).
- A specific operation returns `SPEC_VERSION_MISMATCH` repeatedly.
- Before tagging a CLI release.
- Adding a new section that wasn't previously covered (rare).

The maintainer pipeline (`docs/build-pipeline.md`) automates the re-pull + diff + code-gen + classification check loop.
