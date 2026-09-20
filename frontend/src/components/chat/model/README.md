# `model/` — the local provider-neutral chat model

Layer 2 of the chat render pipeline. A provider plugin (layer 1) reads its own
wire format and produces these types; the shared renderers (layer 3) draw them.
Nothing here knows a provider, and nothing here draws.

**The tool pair.** A provider reads each tool call into ONE `ToolCall`
(`toolCall.ts` and `createToolCall.ts`): a kind from the closed `ToolKind` set, a typed request, one
result slot, and the shared envelope. `tools/` holds the request/result type
table, total over `ToolKind`; `row.ts` wraps a call with where its row sits in
the span. There is no second, row-shaped tool model -- the migration that ran
beside it is finished, and these modules are what remains. The lifecycle's
illegal status/result pairs are compile-time contracts: `toolCall.typecheck.ts`
holds each one as an `@ts-expect-error` with its legal counterpart beside it,
so a pair the union refuses fails `tsc`, not a runtime walk.

**Where a shape lives.** `tools/<kind>.ts` is one kind's home, and
`chatLayerStructure.test.ts` pins that one-to-one. A shape that SEVERAL kinds compose sits
one directory up instead, at `model/` top level, and the kind files alias it:
`fileEditDiff.ts` under the four file-change kinds, `commandResult.ts` under
`execute`, `searchResult.ts` under the three search kinds and `list`,
and `mcpToolCall.ts` under the generic trio. So does
a shape that carries a PARSER rather than only a type, because a reader outside the
tool pipeline needs it: `readFileResult.ts` is the file viewer's content parse as
well as `ReadResult`, and `chartResult.ts` is a chart specification parser.

A one-line `tools/<kind>.ts` that aliases such a shape is therefore the rule working,
not an indirection to remove. `tools/index.ts`, `tools/generic.ts` and
`tools/fileChange.ts` are the shared modules inside `tools/` itself, all three listed
in that guard.

**Dependency rule.** A module in this directory takes a VALUE import from
`~/lib/*`, `~/generated/*`, `~/models/*`, anything `model/` owns at any depth,
and the three pure diff modules `../diff/diffBuilder`, `../diff/diffTypes` and
`../diff/unifiedDiffParser`. It takes a TYPE import from the same roots.

It imports a provider, a result, a control, a component, a store, a stylesheet,
or an icon library in no form. This restriction includes type imports. The
`chat-pipeline/layer-imports` rule checks each import form and canonical path.

- A VALUE import of a component closes an import cycle, which the bundler reports
  as an unrelated module failing to load.
- A presentation or store TYPE costs nothing at run time and is still wrong: it
  makes a render-layer decision part of what a row MEANS. `ReadReminder` carried
  the `Alert` component's `AlertVariant`, and a tool call carried a `LucideIcon`.
  Each is now a closed model-owned union that `results/` maps onto the component --
  `ReminderSeverity`, `ToolIconHint`, `NotificationIconHint`. A neutral model both
  layers share lives in `~/models/` instead, which is where `TodoItem` went.
