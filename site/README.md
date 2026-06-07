# LeapMux documentation site

The source for <https://leapmux.dev>, a static site built with
[Hugo](https://gohugo.io/) and the [Hextra](https://imfing.github.io/hextra/)
theme. The LeapMux user manual is served from `/docs/`.

## How it works

- **Hugo is not installed system-wide.** It is declared as a Go tool dependency
  in [`go.mod`](go.mod) (`tool github.com/gohugoio/hugo`) and invoked with
  `go tool hugo`, exactly like `sqlc` and `golangci-lint` elsewhere in this
  repository.
- **Hextra is a Hugo module.** It is imported in [`hugo.yaml`](hugo.yaml) and
  pinned in `go.mod` (`github.com/imfing/hextra`). Hugo resolves it from the Go
  module cache at build time.
- This module is intentionally **kept out of the root `go.work`** so its large
  dependency graph does not leak into the backend/desktop workspace. All
  commands run with `GOWORK=off`.

## Building

From the repository root:

```bash
task site            # builds the site into site/public/
```

That runs `cd site && GOWORK=off go tool hugo --gc --minify`. The first run
compiles Hugo from source (cached afterwards) and downloads the Hextra module,
so it needs network access.

## Local preview

From the repository root:

```bash
task dev-site        # live-reloading dev server
```

That runs `cd site && GOWORK=off go tool hugo server --buildDrafts`. Then open
<http://localhost:1313>. Edits to `content/`, `hugo.yaml`, or `static/` reload
automatically.

## Structure

```
site/
├── go.mod              # Hugo (as a go tool) + Hextra (as a Hugo module)
├── hugo.yaml           # Site configuration (baseURL, menu, theme params)
├── content/
│   ├── _index.md       # Home page (https://leapmux.dev/)
│   └── docs/           # User manual (https://leapmux.dev/docs/)
│       ├── _index.md   # Manual landing page
│       └── NN-*.md     # Manual chapters (ordered by `weight`)
└── static/             # Logo and favicon
```

To add or edit a chapter, drop a Markdown file in `content/docs/` with front
matter that sets `title`, `type: docs`, and a `weight` (controls sidebar order).
Internal links use absolute paths, e.g. `[Configuration](/docs/18-configuration/)`.
