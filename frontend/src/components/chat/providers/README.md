# `providers/` — the provider plugins

Layer 1 of the chat render pipeline. A plugin reads ONE agent's own wire format and
returns the provider-neutral model (`../model/`, layer 2); the shared renderers
(`../results/`, layer 3) draw it. A plugin never draws a transcript row, and shared
code never parses a provider's bytes.

`registry.ts` holds the `Provider` interface every plugin fills. It mirrors the
backend's `agent.Provider`, and each side carries the hooks its own layer needs.

## Where a module goes

Two locations, and one question decides between them: **does this module turn the
provider's bytes into model?**

- **`extractors/`** — yes. Every reader that answers a `ToolCall`, a `ChatRow`, a
  `TurnEnd` or a `NotificationEntry`.
- **the provider's root** — no. Plugin registration, the control path (a permission
  prompt, a question form, an elicitation), the wire vocabulary, and the settings.

## The module names

One name for each job, in every provider that has that job. A provider omits the file
when it has no such job; it never renames it.

| File | What it holds |
|---|---|
| `plugin.ts` | The `registerProvider` call, and nothing else that another module can hold. |
| `protocol.ts` | Wire tokens only the frontend interprets. Shared tokens live in a generated contract. |
| `toolKinds.ts` | The wire name to `ToolKind` table (`<PROVIDER>_TOOL_KINDS`) and the lookup over it. |
| `toolNames.ts` | The tool NAME vocabulary (`<PROVIDER>_TOOL_NAMES`) and its aliases. |
| `classification.ts` | Which shared message category one frame takes. |
| `extractControl.ts` | One control request read into `ControlPrompt`. |
| `controlResponse.ts` | How an answered control request reads back. |
| `elicitation.ts` | One elicitation form read into `ElicitationRequest`. |
| `askUserQuestion.ts` | The question-request half of the control path: recognize, read, answer. |
| `resolveMessage.ts` | The `Provider.resolveMessage` hook: supplemental content merged into the payload for DISPLAY. |
| `toolSupplement.ts` | The stored supplement read into a typed struct. Input to extraction, not model. |
| `resumeHandle.ts` | The `Provider.validateResumeHandle` hook: which resume handles this provider accepts. |
| `<Provider><Component>.tsx` | A control surface this provider answers itself. The file carries its component's name. |
| `toolResults.fixtures.ts` | Test DATA for the sibling `toolResults.test.ts`. Nothing that ships imports it. |
| `extractors/row.ts` | The `extractRow` entry: one transcript row into `ChatRow`. |
| `extractors/toolCall.ts` | The tool-call entry: one call into `ToolCall`, as `<provider>ToolCall`. |
| `extractors/<kind>.ts` | One `ToolKind`'s reader, named for the kind it answers — `agent.ts`, `read.ts`, `todo.ts`. |
| `extractors/notification.ts` | One notification frame into `NotificationEntry[]`. |
| `extractors/resultDivider.ts` | One turn-end frame into `TurnEnd`. |
| `extractors/toolCommon.ts` | The row shape the kind readers beside it share. |
| `testUtils.tsx` | The shared test helpers a provider FAMILY reuses. `acp/` and `zcode/` hold one each. |

**`extractors/search.ts` answers a FAMILY of kinds, and it is the one file that does.**
`grep`, `glob` and `search` read one output format, and the tool that ran is what picks
between the three. Which of them a provider sends differs -- `acp/` answers `search`
alone, and `claude/`, `pi/` and `zcode/` answer `grep` and `glob` -- so a split by kind
would give the reader three files for one format, and a different three in each
provider. No other kind shares a reader this way.

**There is no `renderers/` directory, and a guard refuses one.** Four providers carried
one until the pipeline closed. Every module in them answered model rather than markup, so
the name sent each reader to the wrong layer. They are `extractors/` now.

**Codex has no `extractors/toolCall.ts`, and that is the shape of its protocol.** One
Codex item carries the arguments and result data. The request frame and the result
frame differ only in status -- so reading the row and reading the call are one read, and
`extractors/row.ts` does both. A second module there would be a boundary the data does
not have. Kilo has none either, and for a different reason: it holds no `extractors/`
directory at all, so the OpenCode family adapter reads its calls. Every other provider
sends the two halves separately.

**Codex has no `toolKinds.ts`, because it sends no tool name.** The item TYPE is its
whole tool identity, and `CodexToolFacts.type` says so at its declaration. Three facts
follow from that.

- The kind is a function of the WHOLE item, not a lookup in a table. `codexItemKind`
  reads `item.changes` for a file change, and the action for a web search.
- `codexItemKind` stays in `extractors/row.ts`, because it calls
  `extractors/webSearch.ts`. Every other vocabulary module runs the other way: an
  extractor imports it, and it imports no extractor.
- `itemVocabulary.ts` holds the vocabulary that IS a table and that the browser alone
  reads: the status words, and the one category that rides on a method. The item types
  and the JSON-RPC method names cross the language boundary, so
  `~/generated/contracts/codex-protocol` holds them and a call site imports them from
  there. `itemVocabulary.test.ts` walks the generated item types the way each
  `toolVocabulary.test.ts` walks a tool-name table.

## The families

Each provider that speaks the Agent Client Protocol registers through a shared entry
rather than calling `registerProvider` itself.

- `acp/registerACPProvider.ts` — Cursor, Goose, Reasonix, Qwen Code, Grok Build and
  Kiro.
- `registerOpenCodeProtocolProvider.ts` — OpenCode and Kilo, which store the plan-mode
  axis in an option group instead of `permissionMode`.

The other providers call `registerProvider` in their own `plugin.ts`:

- Claude.
- Codex.
- Copilot.
- Pi.
- ZCode.
- Codewhale.
- Kimi Code.
- MiMo Code.
- Oh My Pi.
- Amp.
- Cline.

**Amp is not a member of the Claude family**, although its stream-JSON lines resemble
Claude Code's. The `result` line ends a PROCESS rather than a turn, a tool result is a
string, and the permission banner reads an envelope that LeapMux writes, so `amp/`
shares no reader with `claude/`.

**Copilot is not an Agent Client Protocol provider**, although
its adapter shape resembles one. It reaches the browser as native session events
rather than as the session updates `acp/` reads, `copilot/protocol.ts` unwraps them,
and the worker asserts its arguments hold no `--acp`.

**MiMo Code is not a member of the OpenCode family**, although MiMo is a fork of
OpenCode. The worker drives MiMo's own HTTP server and persists its native events, so
`mimo/` reads MiMo's message parts and not session updates. Two tool names also mean
something different than in OpenCode: `task` is the to-do tool, and `actor` starts a
subagent.
The plugin shares one thing with OpenCode, the question vocabulary in
`~/generated/contracts/opencode-protocol`, because MiMo kept OpenCode's question tool.

**Oh My Pi shares no code with Pi**, although omp began as a fork of Pi. Its RPC
events, commands and session files differ from Pi's, so `ohmypi/` reads omp's own
frames, and the worker drives omp through a package of its own.

A family member reads its own calls through an `ACPToolCallAdapter`. The shared build
in `acp/extractors/toolCall.ts` answers everything that adapter does not. Three shapes
carry it.

- **Its own module.** Cursor, Goose, Reasonix, Qwen Code and Grok Build each export
  `<provider>ToolCallAdapter` from `extractors/toolCall.ts`.
- **A factory.** OpenCode exports `openCodeToolCallAdapterFor`. It takes an extra
  `ToolKind` lookup and returns the adapter.
- **A table alone.** Kilo holds no `extractors/` directory. It supplies `kiloToolKind`
  from `toolKinds.ts`, and `registerOpenCodeProtocolProvider` passes that to
  `openCodeToolCallAdapterFor`.

### The adapter contract

`ACPToolCallAdapter` is `(facts, base) => ToolCallSpec`. The members follow rules
that the signature does not state, and these are those rules.

**`base()` is OPTIONAL, and so is the parameter.** `base()` answers the shared build at
the kind the WIRE stated, with the generic trio folded to `mcp`. A member that wants
that build calls it. A member that answers from its own frame does not, and two members
never do. Reasonix declares the parameter as `_base` to say so. OpenCode's factory
returns an adapter of one parameter, which says the same thing. Cursor declares one
parameter and rebuilds the base itself. It replaces the frame's text with the saved
record's output first, so the supplied closure holds facts that Cursor already left
behind.

**Each member hooks a different fact, and that is why the members differ.** The list is
short on purpose: a reading that the frame supplies in a provider-NEUTRAL way belongs in
the shared build, where every member reads it.

- Cursor hooks the tool NAME. Five tools write their own name into `rawInput`.
- OpenCode hooks the registry id the daemon sends as the call title, and the display
  metadata it keeps beside the output.
- Goose hooks the `_meta.goose.toolCall` record. Its platform extensions send no
  prefix, so that record is the only statement of the tool name.
- Reasonix hooks the `use_capability` envelope, and unwraps the call inside it.
- Qwen Code hooks `_meta.toolName`, and Grok Build hooks `_meta["x.ai/tool"]`. Each
  titles its calls in prose, so the tool name is only in that record.
- Kiro hooks the call TITLE, which identifies each built-in tool, and `_meta.kiro`,
  which identifies a subagent, a question and an MCP call. A shell call takes the
  model's description as its title, so Kiro reads every `execute` call as a command.
  The one exception is a `Control Process` call, which states an action.
- Kilo hooks nothing of its own. It supplies a table and reuses OpenCode's adapter.

**A member that repairs the KIND does it before the build, from its own table or its
own reader.** Cursor calls `cursorSearchKind` for a wire `search`, which the title and
the counters narrow. OpenCode calls `openCodeCallKind`, which also reports whether the
ANSWER may narrow the kind again. Goose and Reasonix each look the kind up in their own
`toolKinds.ts` table, behind a predicate that keeps the entry's literal type. Kilo
supplies `kiloToolKind` to the factory. No member reads the kind out of branch order
alone.

**`acpRemapFacts` is the supported route to re-derive the facts under a new kind. A
hand-written `{...facts}` is NOT.** `acpToolFacts` derives `args`, `text` and `images`
from the KIND and from the CONTENT, so a clone keeps all three at their pre-remap
values. `reasonix/extractors/toolCall.ts` records what that cost at its own site. The
text stayed the text of the original frame. A completed `read_file` whose wire content
is empty then fell back to that stale empty string, and it drew no result body at all.
`args` and `images` fail the same way: the `locations` path recovery never runs, and the
pictures stay the pictures of the original `rawInput`.

**A post-condition wraps the whole build.** Cursor holds the only one today. Its adapter
calls a private `cursorToolCall`, then applies two rules to the answer. One rule reads
the refused call that `rawOutput` reports. The other reads the protocol failure in
`rawOutput.error`. The wrapper is where a rule that holds for EVERY row of one provider
belongs, because the build inside it has a dozen returns. It is also the only place a
family-wide rule can live today. One more such rule needs a wrapper of the same shape
around each member.

## The guards

- The `chat-pipeline` ESLint plugin checks layer imports, shared provider decisions,
  forbidden correlated assertions, and plugin hook registration.
- `src/test-support/chatLayerStructure.test.ts` — the structure those lint rules
  assume: the module-name rules above (a PascalCase `.tsx` carries its
  component's name, and no `renderers/` directory comes back), every `classify`
  hook arriving from a `classification.ts` module, one `model/tools/<kind>.ts` file
  per `ToolKind`, and every exception path the lint config carves out pinned to
  a file that exists. It resolves each `classify` hook to the module it arrives
  from, and that module must be a `classification.ts`. It reads that one
  property, so a sub-hook such as `classifyToolCallUpdate` stays out of scope.
