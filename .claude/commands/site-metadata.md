---
description: Audit and refresh the docs site's SEO/social metadata (descriptions, keywords, OG cards, JSON-LD) and verify the build
allowed-tools:
  - Bash
  - Read
  - Write
  - Edit
  - Glob
  - Grep
---

# Refresh docs-site metadata

You are auditing and refreshing the SEO / social metadata for the LeapMux
documentation site under `site/`.

## What is automatic vs. manual

Most of this metadata is **generated at build time** and needs no maintenance:

- **OG/Twitter card images** — `site/layouts/_partials/og-image.html` overlays each
  page's title onto `site/assets/og/base.png` with the Inter font. Cards are written to
  `site/public/og/` (gitignored), one per page, regenerated on every build. **Deleted
  pages drop their card automatically; `hugo --gc` cleans the image cache.** There are no
  committed per-page card files to update or remove.
- **JSON-LD** (`schema.html`) and **keywords** (`keywords.html`) — derived from each
  page's title, section, dates, and front matter at build time.

The **only** hand-authored, per-page artifact is the front-matter `description`. So this
command is an audit + authoring pass over descriptions, plus a full verification build that
also catches template regressions.

Do NOT edit generated output under `site/public/` — it is rebuilt from `site/content`,
`site/layouts`, and `site/assets`.

## Step 1: Audit descriptions

Find every content page and flag problems. Run:

```bash
cd site
for f in $(find content/docs -name '*.md' | sort); do
  d=$(grep -m1 '^description:' "$f" | sed 's/^description: *"//; s/"$//')
  if [ -z "$d" ]; then echo "MISSING  $f"
  elif [ ${#d} -gt 160 ]; then echo "LONG(${#d}) $f"
  else echo "ok(${#d})  $f"; fi
done
```

Also check for duplicate descriptions (two pages sharing one is a copy-paste smell):

```bash
grep -rh '^description:' content/docs | sort | uniq -d
```

Flag: pages with **no** `description`, descriptions **over 160 characters** (they truncate
in search results), and duplicates.

## Step 2: Fix the flagged pages

For each flagged page, read its opening so the description is accurate, then add or rewrite
the `description` front matter (right after `title:`). Write a concise, specific,
preview-tuned line, **120-160 characters**, plain ASCII (no smart quotes/em-dashes), no
double quotes inside the value.

Conventions to follow (see `site/CLAUDE.md` and the docs-site memory):

- **Pre-Concepts pages** — the manual home (`content/docs/_index.md`),
  `getting-started/introduction.md`, and `getting-started/_index.md` — must NOT use the
  architecture terms Hub / Worker / Frontend. Use plain language there.
- Capitalize Hub / Worker / Frontend elsewhere; keep run modes (solo/distributed) lowercase.
- The global fallback (`params.description` in `hugo.yaml`) covers the home page and the
  `llms.txt` header — update it if the product pitch changes.

To set or override keywords for a page (rarely needed; derivation is usually fine), add a
front-matter `keywords` list — it overrides the derived set in `keywords.html`.

## Step 3: Build and verify

```bash
cd site && GOWORK=off go tool hugo --gc --minify 2>&1 | tail -5
```

The build must be clean. Then verify, from `site/`:

1. **Tags present** on a docs page, a section index, and the home page:
   ```bash
   sed -n '/<head>/,/<\/head>/p' public/docs/using/workspaces/index.html \
     | grep -oE '<meta (property|name)=[^>]*(og:|twitter:|description|keywords)[^>]*>'
   ```
   Expect: `og:image`, `og:image:width/height`, `twitter:card=summary_large_image`,
   `twitter:image`, `og:site_name`, non-empty `og:description` and `<meta description>`,
   and a correct `article:section`.

2. **JSON-LD is valid on every page** (catches template regressions):
   ```bash
   for f in $(find public -name index.html); do
     j=$(grep -oE '<script type=application/ld\+json>[^<]*</script>' "$f" | sed -E 's#</?script[^>]*>##g')
     [ -z "$j" ] && continue
     echo "$j" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{JSON.parse(s)}catch(e){console.log("INVALID",e.message);process.exit(1)}})' || echo "  ^ $f"
   done
   ```
   Pages carry `TechArticle` + `BreadcrumbList`; the home page carries `WebSite` +
   `Organization`.

3. **A card looks right** — extract one page's `og:image`, copy it out, and view it:
   ```bash
   OG=$(grep -oE 'property="og:image" content="[^"]*"' public/docs/using/workspaces/index.html | head -1 | sed -E 's/.*content="([^"]*)".*/\1/')
   cp "public/${OG#https://leapmux.dev/}" /tmp/card.png
   ```
   Read `/tmp/card.png`: legible title, correct section eyebrow, "LeapMux" wordmark
   (not LEAPMUX), `leapmux.dev/docs` footer, 1200x630. If a long title overflows, tune
   `$maxChars` / `$titleSize` in `og-image.html`.

4. **llms.txt** exists and is hierarchical: `head -25 public/llms.txt`.

5. **"Last updated on"** shows a git date and no author on a docs page:
   ```bash
   grep -o 'Last updated on[^<]*<time[^>]*>[^<]*</time>' public/docs/reference/legal/index.html | head -1
   ```

6. **No broken numeric-prefix links** left from the old structure:
   ```bash
   grep -roE '/docs/[0-9]+-[a-z-]+/' public/ | sort -u
   ```
   (empty = good)

## Step 4: Report

Summarize: pages audited, descriptions added/rewritten, any keyword overrides, the
verification results (build, JSON-LD count, card check, llms.txt, last-updated), and
anything that still needs human judgment. Do not commit unless asked.
