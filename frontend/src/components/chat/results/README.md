# `results/` — the shared renderers

Layer 3 of the chat render pipeline. A provider plugin (`../providers/`, layer 1) reads
its own wire format into the provider-neutral IR (`../ir/`, layer 2); everything here
draws that IR. Nothing here knows a provider, and nothing here parses a provider's
bytes.

**The kind table.** `tools/index.ts` holds `TOOL_KIND_RENDERERS`, total over `ToolKind`,
and `rendererFor` reads it. `tools/<kind>.tsx` is one kind's renderer, exported as
`<kind>Renderer` and typed `ToolKindRenderer<'<kind>'>`. `tools/renderer.ts` declares
that interface. A kind added to `TOOL_KINDS` without its file is a compile error.

Three modules in `tools/` build a renderer rather than being one:

- `generic.tsx` supplies the generic trio.
- `proseResult.tsx` supplies every kind whose result is words.
- `fileChanges.tsx` supplies the four kinds whose request and result are file changes,
  and it holds the rule that a failed call draws its reason and never the diff it
  asked for.

Each one is called at the top of the kind module that uses it, so the export in every
`<kind>.tsx` is still a renderer.

## Module names

Two conventions, and the file extension does not pick between them — the module's job
does.

- **camelCase**, named after the IR shape it draws, so the pair reads by sight:
  `ir/searchResult.ts` and `results/searchResult.tsx`, `ir/commandResult.ts` and
  `results/commandResult.tsx`. The body it exports takes a `*Body` suffix
  (`SearchResultBody`), because the kind renderer above it supplies the title and the
  chrome.
- **PascalCase**, named for ONE reusable component, exactly as `../widgets/` does:
  `ToolMessage.tsx`, `CollapsibleContent.tsx`, `ToolStatusHeader.tsx`. The file carries
  that component's own name, and
  `src/test-support/chatLayerModuleNames.test.ts` fails the suite when it does not.

A body that several kinds share sits here; a body that one kind draws can sit in its
own `tools/<kind>.tsx`.

## What layer 3 may know

It reads the IR and the design tokens. It never imports `../providers/`, and
`providerLayering.test.ts` enforces the other direction — a plugin that imports from
here puts a parser the whole pipeline depends on behind a module that exists to draw.
Seven providers reached into `readFileResult.tsx` for its content parser before that
rule existed, so the content parse now lives in `ir/readFileResult.ts` and both layers
read it from there.
