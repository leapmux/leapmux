import { execFileSync } from 'node:child_process'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { beforeAll, describe, expect, it } from 'vitest'
import { PROVIDER_FRAME_KINDS } from '~/generated/contracts/provider-frame-kinds'
import { PROVIDER_WIRE_TOKENS, wireTokenSources } from '../../eslint/providerWireTokens'

// ESLint REPLACES the options of a rule. It never merges them.
//
// So a config block that sets `no-restricted-syntax` for a subset of the tree
// deletes every selector that an earlier block put there. The deletion is
// silent: the new selector works, `eslint .` passes, and the lost selectors
// leave no message anywhere. That already happened once. The block that bans
// `title` on a DOM element scoped itself to `src/**` and `tests/**` -- the only
// two trees that ship -- and dropped antfu's `const enum` and `export =` bans
// in exactly those trees, while a root-level file such as `vitest.config.ts`
// kept them.
//
// The guard resolves the REAL config through ESLint, for one file in each
// scoped tree, and requires the selector set to stay a superset of the
// baseline. A file at the repo root is the baseline, because no block below
// antfu's own configuration matches it. Thus the guard covers a selector that
// antfu adds later too, with no edit here.

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')

/** A file that no scoped block matches. It carries antfu's options alone. */
const BASELINE_FILE = 'vitest.config.ts'

/** One file inside each tree that the scoped blocks match. */
const SCOPED_FILES = ['src/app.tsx', 'tests/e2e/helpers/mail.ts']

/**
 * One file inside each chat tree the blocks in `eslint.config.ts` scope, with a
 * selector that must be present for it. A scoped block that stops matching its
 * tree -- a path typo, a files pattern the real tree does not spell -- leaves
 * the architecture rules reading green while guarding nothing.
 */
interface LintSample {
  file: string
  label: string
  source: string
}

/**
 * Imports across a layer boundary. Each probe imports a leaf module of the other layer,
 * a module with no imports of its own. `chat-pipeline/layer-imports` and the import
 * patterns read the path alone, so any module of the layer proves the rule. The parser
 * builds the type information of each probe edit from every module that the probe
 * imports. A probe of `providers/registry` thus put the whole provider tree into each
 * update, which cost about ten seconds of the setup.
 */
const RESTRICTED_IMPORT_SAMPLES: LintSample[] = [
  { label: 'model static type import', file: 'src/components/chat/model/auditProbe.ts', source: 'import type { TOOL_FILE_PATH_KEYS } from \'../providers/toolInputKeys\'' },
  { label: 'model re-export', file: 'src/components/chat/model/auditProbe.ts', source: 'export * from \'../providers/toolInputKeys\'' },
  { label: 'model side-effect import', file: 'src/components/chat/model/auditProbe.ts', source: 'import \'../providers/toolInputKeys\'' },
  { label: 'model dynamic import', file: 'src/components/chat/model/auditProbe.ts', source: 'void import(\'../providers/toolInputKeys\')' },
  { label: 'model computed dynamic import', file: 'src/components/chat/model/auditProbe.ts', source: 'void import(modulePath)' },
  { label: 'model require call', file: 'src/components/chat/model/auditProbe.ts', source: 'require(\'../results/collapse\')' },
  { label: 'model import-equals declaration', file: 'src/components/chat/model/auditProbe.ts', source: 'import collapse = require(\'../results/collapse\')' },
  { label: 'model import type', file: 'src/components/chat/model/auditProbe.ts', source: 'type ProviderModule = typeof import(\'../providers/toolInputKeys\')' },
  { label: 'provider side-effect import', file: 'src/components/chat/providers/auditProbe.ts', source: 'import \'../results/collapse\'' },
  { label: 'provider dynamic import', file: 'src/components/chat/providers/auditProbe.ts', source: 'void import(\'../results/collapse\')' },
  { label: 'provider require call', file: 'src/components/chat/providers/auditProbe.ts', source: 'require(\'../results/collapse\')' },
  { label: 'provider computed require call', file: 'src/components/chat/providers/auditProbe.ts', source: 'require(modulePath)' },
  { label: 'provider import-equals declaration', file: 'src/components/chat/providers/auditProbe.ts', source: 'import collapse = require(\'../results/collapse\')' },
  { label: 'provider import type', file: 'src/components/chat/providers/auditProbe.ts', source: 'type ResultModule = typeof import(\'../results/collapse\')' },
  { label: 'result type re-export', file: 'src/components/chat/results/auditProbe.ts', source: 'export type { TOOL_FILE_PATH_KEYS } from \'../providers/toolInputKeys\'' },
  { label: 'result dynamic import', file: 'src/components/chat/results/auditProbe.ts', source: 'void import(\'../providers/toolInputKeys\')' },
  { label: 'result require call', file: 'src/components/chat/results/auditProbe.ts', source: 'require(\'../providers/toolInputKeys\')' },
  { label: 'result import-equals declaration', file: 'src/components/chat/results/auditProbe.ts', source: 'import keys = require(\'../providers/toolInputKeys\')' },
  { label: 'result import type', file: 'src/components/chat/results/auditProbe.ts', source: 'type ProviderModule = typeof import(\'../providers/toolInputKeys\')' },
]

const RESTRICTED_ASSERTION_SAMPLES: LintSample[] = [
  { label: 'unsafe assertion inside a const object', file: 'src/components/chat/results/auditProbe.ts', source: 'const result = { call: value as ToolCall } as const\nvoid result' },
  { label: 'tool call as assertion', file: 'src/components/chat/results/auditProbe.ts', source: 'value as ToolCall' },
  { label: 'tool call angle-bracket assertion', file: 'src/components/chat/results/auditProbe.ts', source: '<ToolCall>value' },
  { label: 'tool call helper assertion', file: 'src/components/chat/results/auditProbe.ts', source: 'value as ToolCallVariant<\'read\'>' },
  { label: 'qualified tool call assertion', file: 'src/components/chat/results/auditProbe.ts', source: 'value as ChatIR.ToolCall' },
  { label: 'wrapped tool call assertion', file: 'src/components/chat/results/auditProbe.ts', source: 'value as Readonly<ToolCall>' },
  { label: 'imported tool call assertion', file: 'src/components/chat/results/auditProbe.ts', source: 'value as import(\'../model/toolCall\').ToolCall' },
  { label: 'tool payload helper assertion', file: 'src/components/chat/providers/auditProbe.ts', source: 'value as ToolCallSpecVariant<\'read\'>' },
  { label: 'tool request indexed assertion', file: 'src/components/chat/results/auditProbe.ts', source: 'value as ToolRequestByKind[\'read\']' },
  { label: 'qualified tool request indexed assertion', file: 'src/components/chat/results/auditProbe.ts', source: 'value as ChatIR.ToolRequestByKind[\'read\']' },
  { label: 'tool result indexed angle-bracket assertion', file: 'src/components/chat/providers/auditProbe.ts', source: '<ToolResultByKind[\'read\']>value' },
  { label: 'resolved content as assertion', file: 'src/components/chat/results/auditProbe.ts', source: 'value as ResolvedMessageContent' },
  { label: 'resolved content angle-bracket assertion', file: 'src/components/chat/providers/auditProbe.ts', source: '<ResolvedMessageContent>value' },
  { label: 'qualified resolved content assertion', file: 'src/components/chat/providers/auditProbe.ts', source: 'value as Pipeline.ResolvedMessageContent' },
  { label: 'wrapped resolved content assertion', file: 'src/components/chat/providers/auditProbe.ts', source: 'value as Readonly<ResolvedMessageContent>' },
  { label: 'imported resolved content assertion', file: 'src/components/chat/providers/auditProbe.ts', source: 'value as import(\'../rowExtractionTypes\').ResolvedMessageContent' },
]

/**
 * A working directory of a native agent that a file opening real agents makes by hand. The `tests/e2e` block of
 * `eslint.config.ts` refuses a cast to the brand in every spelling, and the import of the brand for unit tests. A block
 * that stops matching its tree leaves the type check as the only guard, and a cast passes it.
 */
const WORKING_DIR_BRAND_SAMPLES: LintSample[] = [
  { label: 'as assertion in a spec', file: 'tests/e2e/auditProbe.spec.ts', source: 'const dir = \'/run\' as ProviderWorkingDir\nvoid dir' },
  { label: 'angle-bracket assertion in a helper', file: 'tests/e2e/helpers/auditProbe.ts', source: 'const dir = <ProviderWorkingDir>\'/run\'\nvoid dir' },
  { label: 'double assertion', file: 'tests/e2e/auditProbe.spec.ts', source: 'const dir = \'/run\' as unknown as ProviderWorkingDir\nvoid dir' },
  { label: 'qualified assertion', file: 'tests/e2e/auditProbe.spec.ts', source: 'const dir = \'/run\' as WorkingDirs.ProviderWorkingDir\nvoid dir' },
  { label: 'imported type assertion', file: 'tests/e2e/auditProbe.spec.ts', source: 'const dir = \'/run\' as import(\'./helpers/providerWorkingDir\').ProviderWorkingDir\nvoid dir' },
  { label: 'wrapped assertion', file: 'tests/e2e/auditProbe.spec.ts', source: 'const dir = \'/run\' as Readonly<ProviderWorkingDir>\nvoid dir' },
  { label: 'unit-test brand in a spec', file: 'tests/e2e/auditProbe.spec.ts', source: 'import { unitWorkingDir } from \'~/test-support/unitWorkingDir\'\nvoid unitWorkingDir' },
  { label: 'unit-test brand by a relative path in a helper', file: 'tests/e2e/helpers/auditProbe.ts', source: 'import { unitWorkingDir } from \'../../../src/test-support/unitWorkingDir\'\nvoid unitWorkingDir' },
]

const ALLOWED_ARCHITECTURE_SAMPLES: LintSample[] = [
  // The module of the brand makes it, and a unit test opens no agent.
  { label: 'working directory brand in its own module', file: 'tests/e2e/helpers/providerWorkingDir.ts', source: 'const dir = \'/run\' as ProviderWorkingDir\nvoid dir' },
  { label: 'working directory assertion in a unit test', file: 'tests/e2e/helpers/auditProbe.test.ts', source: 'const dir = \'/run\' as ProviderWorkingDir\nvoid dir' },
  { label: 'unit-test brand in a unit test', file: 'tests/e2e/helpers/auditProbe.test.ts', source: 'import { unitWorkingDir } from \'~/test-support/unitWorkingDir\'\nvoid unitWorkingDir' },
  { label: 'working directory annotation in a spec', file: 'tests/e2e/auditProbe.spec.ts', source: 'declare function make(): ProviderWorkingDir\nconst dir: ProviderWorkingDir = make()\nvoid dir' },
  { label: 'native block table const assertion', file: 'src/components/chat/providers/auditProbe.ts', source: 'const blocks = { ToolResult: \'tool_result\', Text: \'text\' } as const\nvoid blocks' },
  { label: 'native block table angle const assertion', file: 'src/components/chat/providers/auditProbe.ts', source: 'const blocks = <const>{ ToolResult: \'tool_result\', Text: \'text\' }\nvoid blocks' },
  { label: 'model allowed dynamic diff import', file: 'src/components/chat/model/auditProbe.ts', source: 'void import(\'../diff/diffTypes\')' },
  { label: 'model allowed sibling import type', file: 'src/components/chat/model/auditProbe.ts', source: 'type ToolModule = typeof import(\'./toolCall\')' },
  { label: 'registry resolved content assertion', file: 'src/components/chat/providers/registry.ts', source: 'value as ResolvedMessageContent' },
  { label: 'checked builder tool call assertion', file: 'src/components/chat/model/createToolCall.ts', source: 'value as ToolCall' },
  { label: 'plugin imported related hook', file: 'src/components/chat/providers/probe/plugin.ts', source: 'import { related } from \'./spanRole\'\nconst plugin = { transcript: { relatedMessages: related } }' },
  { label: 'plugin imported hook factory', file: 'src/components/chat/providers/probe/plugin.ts', source: 'import { related } from \'./spanRole\'\nconst plugin = { transcript: { relatedMessages: related() } }' },
  { label: 'registration factory parameter hook', file: 'src/components/chat/providers/probe/registerProbeProvider.ts', source: 'export function registerProbeProvider(opts: { spanRole: () => string }) {\n  const plugin = { transcript: { spanRole: opts.spanRole } }\n  return plugin\n}' },
  { label: 'imported capability helper', file: 'src/components/chat/auditProbe.ts', source: 'import { agentTabSupportsInterrupt } from \'~/stores/tab.helpers\'\nvoid agentTabSupportsInterrupt(undefined)' },
  // A wire token matches the whole literal. A word that only CONTAINS a token is not
  // a token, and a match that lost an anchor would reject it.
  { label: 'word that starts with a wire token', file: 'src/components/chat/auditProbe.ts', source: 'const key: string = \'item.startedAt\'\nvoid key' },
  { label: 'word that ends with a wire token', file: 'src/components/chat/auditProbe.ts', source: 'const key: string = \'last.tool.result\'\nvoid key' },
  // Shared code spells ordinary words for its own meaning, although a provider can send
  // the same word as a frame kind.
  { label: 'frame kind that is an ordinary word', file: 'src/components/chat/auditProbe.ts', source: 'const status: string = \'error\'\nvoid status' },
  { label: 'key under a prefix with an ordinary stem', file: 'src/components/chat/auditProbe.ts', source: 'const key: string = \'model.row\'\nvoid key' },
  { label: 'path under a namespace with an ordinary stem', file: 'src/components/chat/auditProbe.ts', source: 'const key: string = \'cursor/pointer\'\nvoid key' },
]

const PROVIDER_DECISION_SAMPLES: LintSample[] = [
  { label: 'provider alias comparison', file: 'src/components/chat/auditProbe.ts', source: 'import { AgentProvider } from \'~/generated/proto/leapmux/v1/agent_pb\'\nconst selected: AgentProvider = AgentProvider.CODEX\nvoid (selected === AgentProvider.CLAUDE_CODE)' },
  { label: 'provider array includes', file: 'src/components/chat/auditProbe.ts', source: 'import { AgentProvider } from \'~/generated/proto/leapmux/v1/agent_pb\'\nconst providers: AgentProvider[] = [AgentProvider.CODEX]\nvoid providers.includes(AgentProvider.CLAUDE_CODE)' },
  { label: 'provider set has', file: 'src/components/chat/auditProbe.ts', source: 'import { AgentProvider } from \'~/generated/proto/leapmux/v1/agent_pb\'\nconst providers = new Set<AgentProvider>()\nvoid providers.has(AgentProvider.CODEX)' },
  { label: 'provider map get', file: 'src/components/chat/auditProbe.ts', source: 'import { AgentProvider } from \'~/generated/proto/leapmux/v1/agent_pb\'\nconst providers = new Map<AgentProvider, string>()\nvoid providers.get(AgentProvider.CODEX)' },
  { label: 'provider table lookup', file: 'src/components/chat/auditProbe.ts', source: 'import { AgentProvider } from \'~/generated/proto/leapmux/v1/agent_pb\'\nconst selected: AgentProvider = AgentProvider.CODEX\nconst labels: Partial<Record<AgentProvider, string>> = {}\nvoid labels[selected]' },
  { label: 'provider switch alias', file: 'src/components/chat/auditProbe.ts', source: 'import { AgentProvider } from \'~/generated/proto/leapmux/v1/agent_pb\'\nconst selected: AgentProvider = AgentProvider.CODEX\nswitch (selected) { case AgentProvider.CODEX: break }' },
  { label: 'destructured provider comparison', file: 'src/components/chat/auditProbe.ts', source: 'import { AgentProvider } from \'~/generated/proto/leapmux/v1/agent_pb\'\nconst { CODEX: codex, CLAUDE_CODE: claude } = AgentProvider\nvoid (codex === claude)' },
  { label: 'imported provider helper', file: 'src/components/chat/auditProbe.ts', source: 'import { AgentProvider } from \'~/generated/proto/leapmux/v1/agent_pb\'\nimport { isCodexProvider } from \'~/test-support/lintFixtures/providerDecision\'\nconst selected: AgentProvider = AgentProvider.CODEX\nvoid isCodexProvider(selected)' },
  { label: 'grok wire token', file: 'src/components/chat/auditProbe.ts', source: 'void (\'_x.ai/ask_user_question\')' },
  { label: 'qwen wire token', file: 'src/components/chat/auditProbe.ts', source: 'void (\'_qwencode/end_turn\')' },
  { label: 'kiro wire token', file: 'src/components/chat/auditProbe.ts', source: 'void (\'_kiro/userInput\')' },
  { label: 'aliased imported provider helper', file: 'src/components/chat/auditProbe.ts', source: 'import { AgentProvider } from \'~/generated/proto/leapmux/v1/agent_pb\'\nimport { isCodexProvider } from \'~/test-support/lintFixtures/providerDecision\'\nconst selected: AgentProvider = AgentProvider.CODEX\nconst matchesProvider = isCodexProvider\nvoid matchesProvider(selected)' },
  { label: 'bare Kimi Code event type', file: 'src/components/chat/auditProbe.ts', source: 'const type: string = \'tool.call.started\'\nvoid type' },
  { label: 'bare Kimi Code approval event type', file: 'src/components/chat/auditProbe.ts', source: 'const type: string = `event.approval.requested`\nvoid type' },
  { label: 'bare MiMo Code event type', file: 'src/components/chat/auditProbe.ts', source: 'const type: string = \'message.part.updated\'\nvoid type' },
  { label: 'bare Oh My Pi frame type', file: 'src/components/chat/auditProbe.ts', source: 'const type: string = \'subagent_lifecycle\'\nvoid type' },
  { label: 'bare Codewhale event name', file: 'src/components/chat/auditProbe.ts', source: 'const event: string = \'approval.required\'\nvoid event' },
]

/** A bare wire token in shared chat code, which `no-provider-decision` must reject. */
function wireTokenSample(label: string, token: string): LintSample {
  const quoted = `'${token.replaceAll('\\', '\\\\').replaceAll('\'', '\\\'')}'`
  return { label, file: 'src/components/chat/auditProbe.ts', source: `const type: string = ${quoted}\nvoid type` }
}

/**
 * Wire tokens spelled by hand: each kind of match, and known frames of each provider.
 * The lint reads the same generated list as the samples below, so these hold the list
 * to known tokens. A table that loses its `frameKind` mark fails here with the name of
 * a frame, not only as a shorter list.
 */
const WIRE_TOKEN_SAMPLES: LintSample[] = [
  wireTokenSample('Copilot event', 'session.idle'),
  wireTokenSample('Copilot event family', 'assistant.fusion_step'),
  wireTokenSample('bare Copilot event family', 'session.canvas.'),
  wireTokenSample('OpenCode event', 'question.asked'),
  wireTokenSample('ZCode event', 'session.created'),
  wireTokenSample('ZCode method', 'interaction/requestUserInput'),
  wireTokenSample('Claude compaction boundary', 'compact_boundary'),
  wireTokenSample('ACP update', 'agent_message_chunk'),
  wireTokenSample('Codex method', 'turn/plan/updated'),
  wireTokenSample('Grok method that no contract holds', '_x.ai/future_method'),
  wireTokenSample('bare Kiro namespace', '_kiro/'),
  wireTokenSample('Oh My Pi compaction frame', 'auto_compaction_start'),
  wireTokenSample('Oh My Pi subagent frame', 'subagent_event'),
  wireTokenSample('Oh My Pi question frame', 'leapmux_ask'),
  wireTokenSample('Oh My Pi answer frame', 'leapmux_ask_answer'),
  wireTokenSample('Kimi Code tool delta', 'tool.call.delta'),
  wireTokenSample('Kimi Code tool result', 'tool.result'),
  wireTokenSample('Kimi Code turn start', 'turn.started'),
  wireTokenSample('Kimi Code turn step', 'turn.step.completed'),
  wireTokenSample('Kimi Code text delta', 'assistant.delta'),
  wireTokenSample('Kimi Code question event', 'event.question.requested'),
  wireTokenSample('MiMo Code status event', 'session.status'),
  wireTokenSample('MiMo Code permission event', 'permission.asked'),
  wireTokenSample('Codewhale item event', 'item.completed'),
  wireTokenSample('Codewhale steering event', 'turn.steer_dropped'),
  wireTokenSample('Codewhale approval timeout', 'approval.timeout'),
  wireTokenSample('Codewhale input event', 'user_input.required'),
  wireTokenSample('Codewhale sandbox event', 'sandbox.denied'),
  wireTokenSample('Codewhale store event', 'runtime.store_failure'),
  wireTokenSample('Cline tool event', 'tool.finished'),
  wireTokenSample('Cline run event', 'run.completed'),
  wireTokenSample('Cline text event', 'assistant.finished'),
  wireTokenSample('Cline media event', 'assistant.media'),
  wireTokenSample('Cline notice event', 'session.notice'),
  wireTokenSample('Cline team event', 'team.progress'),
  wireTokenSample('Cline approval event', 'approval.requested'),
  wireTokenSample('Cline capability event', 'capability.requested'),
  wireTokenSample('Cline question executor', 'tool_executor.askQuestion'),
]

/**
 * One wire token for each contract table that the lint guards: the first literal of
 * the table that the token index guards. A table whose literals are all ordinary words
 * gives no sample, because the lint leaves those words to shared code. The unit tests
 * of `providerWireTokens.ts` cover the index. These samples prove that the real config
 * applies it to every table.
 */
function contractTableSamples(): LintSample[] {
  const first = new Map<string, string>()
  for (const kind of PROVIDER_FRAME_KINDS) {
    if (!first.has(kind.source) && wireTokenSources(PROVIDER_WIRE_TOKENS, kind.literal).includes(kind.source))
      first.set(kind.source, kind.literal)
  }
  return [...first].map(([source, literal]) => wireTokenSample(`${source}: ${literal}`, literal))
}

const CONTRACT_TABLE_SAMPLES = contractTableSamples()

const REGISTRATION_SAMPLES: LintSample[] = [
  { label: 'plugin inline related hook', file: 'src/components/chat/providers/probe/plugin.ts', source: 'const plugin = { transcript: { relatedMessages: () => [] } }' },
  { label: 'plugin inline role hook', file: 'src/components/chat/providers/probe/plugin.ts', source: 'const plugin = { transcript: { spanRole() { return \'other\' } } }' },
  { label: 'plugin local named hook', file: 'src/components/chat/providers/probe/plugin.ts', source: 'const spanRole = () => \'other\'\nconst plugin = { transcript: { spanRole } }' },
  { label: 'plugin inline control-response hook', file: 'src/components/chat/providers/probe/plugin.ts', source: 'const plugin = { controls: { buildControlResponse() { return {} } } }' },
  { label: 'plugin inline question hook', file: 'src/components/chat/providers/probe/plugin.ts', source: 'const plugin = { controls: { askUserQuestion: { sendAnswer: async () => {} } } }' },
  { label: 'plugin local control hook', file: 'src/components/chat/providers/probe/plugin.ts', source: 'const buildControlResponse = () => ({})\nconst plugin = { controls: { buildControlResponse } }' },
  { label: 'plugin inline session hook', file: 'src/components/chat/providers/probe/plugin.ts', source: 'const plugin = { session: { contextUsageFromMessage: () => null } }' },
  { label: 'plugin inline configuration hook', file: 'src/components/chat/providers/probe/plugin.ts', source: 'const plugin = { configuration: { planMode: { currentMode: () => \'plan\' } } }' },
  { label: 'plugin future inline hook', file: 'src/components/chat/providers/probe/plugin.ts', source: 'const plugin = { transcript: { hookAddedLater: () => null } }' },
  { label: 'registration factory inline role hook', file: 'src/components/chat/providers/probe/registerProbeProvider.ts', source: 'const plugin = { transcript: { spanRole() { return \'other\' } } }' },
]

const ARCHITECTURE_RULE_IDS = new Set([
  'no-restricted-syntax',
  'ts/no-restricted-imports',
  'chat-pipeline/layer-imports',
  'chat-pipeline/no-provider-decision',
  'chat-pipeline/no-forbidden-assertion',
  'chat-pipeline/plugin-registration-only',
])

/** The DOM-`title` ban, which must survive beside the base selectors. */
const TITLE_SELECTOR = 'JSXOpeningElement[name.type="JSXIdentifier"][name.name=/^[a-z]/] > JSXAttribute[name.name="title"]'

/**
 * Resolve `no-restricted-syntax` for each file, and lint each sample, in one
 * SUBPROCESS, so the configuration loads once for every probe.
 *
 * The obvious shape -- import `ESLint` here and call it -- is not available.
 * ESLint loads `eslint.config.ts` through jiti, entirely outside Vite's module
 * graph, so vitest's module runner resolves antfu's plugin tree by rules that
 * are neither Node's nor bun's: eslint-plugin-jsdoc reaches a
 * `jsdoc-type-pratt-parser` build with no ESM named exports and the whole
 * config dies at import with "does not provide an export named 'parse'".
 * Neither `server.deps.external` nor a `resolve.alias` reaches it, for the
 * same reason -- the import never passes through Vite.
 *
 * A subprocess is also the more faithful probe. What this guard asserts is
 * what the LINTER sees, and the linter is a Node process with no jsdom and no
 * Vite. `process.execPath` is the node binary already running vitest.
 */
function inspectEslint(files: readonly string[], samples: readonly LintSample[], ruleIds: ReadonlySet<string>): Inspection {
  // The lint runs only the rules that the assertions read. The rest of antfu's rules
  // cost about half of the lint time and produce messages that no assertion reads.
  // `calculateConfigForFile` ignores the filter, so the selector check still reads
  // the whole resolved rule.
  const script = `
    import { ESLint } from 'eslint'
    import { readFileSync } from 'node:fs'
    const input = JSON.parse(readFileSync(0, 'utf8'))
    const read = new Set(input.ruleIds)
    const eslint = new ESLint({ cwd: process.cwd(), ruleFilter: ({ ruleId }) => read.has(ruleId) })
    const restrictedSyntax = {}
    for (const file of process.argv.slice(1))
      restrictedSyntax[file] = (await eslint.calculateConfigForFile(file)).rules?.['no-restricted-syntax'] ?? null
    const ruleIds = {}
    const parseErrors = {}
    for (const sample of input.samples) {
      const [result] = await eslint.lintText(sample.source, { filePath: sample.file })
      ruleIds[sample.label] = result.messages.map(message => message.ruleId)
      const fatal = result.messages.filter(message => message.fatal || message.ruleId === null).map(message => message.message)
      if (fatal.length > 0)
        parseErrors[sample.label] = fatal
    }
    process.stdout.write(JSON.stringify({ restrictedSyntax, ruleIds, parseErrors }))
  `
  const stdout = execFileSync(
    process.execPath,
    ['--input-type=module', '-e', script, ...files],
    {
      cwd: frontendRoot,
      encoding: 'utf8',
      input: JSON.stringify({ samples, ruleIds: [...ruleIds] }),
      maxBuffer: 32 * 1024 * 1024,
    },
  )
  return JSON.parse(stdout) as Inspection
}

/** What `inspectEslint` reads from the linter. */
interface Inspection {
  /** The resolved `no-restricted-syntax` entry of each file. */
  restrictedSyntax: Record<string, unknown>
  /** The rule ID of each message, by sample label. */
  ruleIds: Record<string, Array<string | null>>
  /** The messages of each sample that the linter could not parse, by sample label. */
  parseErrors: Record<string, string[]>
}

/**
 * Every selector that `no-restricted-syntax` holds for one file.
 *
 * The rule takes each entry either as a bare selector string or as an object
 * with a `selector` field. Both forms appear in this config, so read both.
 */
function selectorsFor(entry: unknown): string[] {
  if (!Array.isArray(entry))
    return []
  return entry
    .slice(1)
    .map(option => (typeof option === 'string' ? option : (option as { selector?: string }).selector))
    .filter((selector): selector is string => typeof selector === 'string')
}

const SAMPLES: LintSample[] = [...RESTRICTED_IMPORT_SAMPLES, ...RESTRICTED_ASSERTION_SAMPLES, ...PROVIDER_DECISION_SAMPLES, ...WIRE_TOKEN_SAMPLES, ...CONTRACT_TABLE_SAMPLES, ...REGISTRATION_SAMPLES, ...WORKING_DIR_BRAND_SAMPLES, ...ALLOWED_ARCHITECTURE_SAMPLES]

describe('no-restricted-syntax keeps the base selectors', () => {
  let resolved: Record<string, unknown>
  let ruleIds: Record<string, Array<string | null>>
  let parseErrors: Record<string, string[]>
  let baseline: string[]

  // The limit is explicit because the default one does not fit the work.
  //
  // This hook boots one Node subprocess. The subprocess loads `eslint.config.ts`
  // through jiti, resolves three files, and lints each probe with the rules
  // that the assertions read. Two costs remain, and neither can shrink:
  //
  // - The load of the real configuration, which is the subject of the guard.
  // - The first type-aware lint. The project service of typescript-eslint
  //   loads the TypeScript project of `src/` before it can place a probe, and
  //   two probes are real files of that project, because their exception is
  //   their path.
  //
  // The hook takes about twenty seconds alone at a load average near 12. A full
  // vitest run starves it: it took 29 seconds at a load average near 25, and it
  // passed 60 seconds at a load average near 40. The limit is sized so that only
  // a hang trips it, not the load of the machine.
  beforeAll(() => {
    const inspection = inspectEslint([BASELINE_FILE, ...SCOPED_FILES], SAMPLES, ARCHITECTURE_RULE_IDS)
    resolved = inspection.restrictedSyntax
    ruleIds = inspection.ruleIds
    parseErrors = inspection.parseErrors
    baseline = selectorsFor(resolved[BASELINE_FILE])
  }, 180_000)

  it('parses every probe', () => {
    // A probe that the linter cannot parse yields no rule message at all, so an
    // "intentional exception" sample passes its check with no rule run on it. A
    // probe path that the type-aware block covers must exist in the project, or
    // be listed in its `allowDefaultProject`.
    expect(parseErrors).toEqual({})
  })

  it('reads a baseline that actually holds selectors', () => {
    // An empty baseline would make the superset check below pass for every
    // tree, including a tree that lost every selector.
    expect(
      baseline.length,
      `${BASELINE_FILE} must resolve to a config with no-restricted-syntax selectors. `
      + 'An empty list means the probe found the wrong file, not that the rule is off.',
    ).toBeGreaterThan(0)
    expect(baseline).toContain('TSEnumDeclaration[const=true]')
    expect(baseline).toContain('TSExportAssignment')
  })

  it.each(SCOPED_FILES)('keeps every baseline selector for %s', (file) => {
    const scoped = selectorsFor(resolved[file])
    const lost = baseline.filter(selector => !scoped.includes(selector))
    expect(
      lost,
      `The config block that sets no-restricted-syntax for ${file} dropped these `
      + 'selectors, because ESLint replaces rule options rather than merging them. '
      + `Spread ANTFU_RESTRICTED_SYNTAX into that block's options:\n  ${lost.join('\n  ')}`,
    ).toEqual([])
  })

  it.each(SCOPED_FILES)('still bans a DOM title for %s', (file) => {
    const scoped = selectorsFor(resolved[file])
    expect(
      scoped.some(selector => selector.includes(TITLE_SELECTOR)),
      `${file} must keep the DOM-\`title\` ban. Without it a bare \`title\` attribute `
      + 'renders the OS tooltip and becomes the accessible name of the element.',
    ).toBe(true)
  })

  it.each(RESTRICTED_IMPORT_SAMPLES)('rejects every layer import form: $label', ({ label }) => {
    expect(ruleIds[label] ?? []).toContainEqual(expect.stringMatching(/^(?:chat-pipeline\/layer-imports|no-restricted-syntax|ts\/no-restricted-imports)$/))
  })

  it.each(RESTRICTED_ASSERTION_SAMPLES)('rejects an unsafe assertion: $label', ({ label }) => {
    expect(ruleIds[label] ?? []).toContainEqual(expect.stringMatching(/^(?:chat-pipeline\/no-forbidden-assertion|no-restricted-syntax)$/))
  })

  it.each(PROVIDER_DECISION_SAMPLES)('rejects a provider decision: $label', ({ label }) => {
    expect(ruleIds[label] ?? []).toContain('chat-pipeline/no-provider-decision')
  })

  it.each(WIRE_TOKEN_SAMPLES)('rejects a bare wire token: $label', ({ label }) => {
    expect(ruleIds[label] ?? []).toContain('chat-pipeline/no-provider-decision')
  })

  it('draws a sample from each contract table that the lint guards', () => {
    // Zero samples would pass the check below for every table, including a list that
    // lost every mark.
    expect(CONTRACT_TABLE_SAMPLES.length).toBeGreaterThan(30)
  })

  it.each(CONTRACT_TABLE_SAMPLES)('rejects a wire token of each contract table: $label', ({ label }) => {
    expect(ruleIds[label] ?? []).toContain('chat-pipeline/no-provider-decision')
  })

  it.each(REGISTRATION_SAMPLES)('rejects an inline plugin hook: $label', ({ label }) => {
    expect(ruleIds[label] ?? []).toContain('chat-pipeline/plugin-registration-only')
  })

  it.each(WORKING_DIR_BRAND_SAMPLES)('rejects a working directory of a native agent that is not made by its rule: $label', ({ label }) => {
    expect(ruleIds[label] ?? []).toContain('no-restricted-syntax')
  })

  it.each(ALLOWED_ARCHITECTURE_SAMPLES)('keeps the intentional exception: $label', ({ label }) => {
    expect((ruleIds[label] ?? []).filter(ruleId => ruleId !== null && ARCHITECTURE_RULE_IDS.has(ruleId))).toEqual([])
  })
})
