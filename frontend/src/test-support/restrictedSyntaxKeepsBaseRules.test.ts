import { execFileSync } from 'node:child_process'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { beforeAll, describe, expect, it } from 'vitest'

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

const RESTRICTED_IMPORT_SAMPLES: LintSample[] = [
  { label: 'model static type import', file: 'src/components/chat/model/auditProbe.ts', source: 'import type { Provider } from \'../providers/registry\'' },
  { label: 'model re-export', file: 'src/components/chat/model/auditProbe.ts', source: 'export * from \'../providers/registry\'' },
  { label: 'model side-effect import', file: 'src/components/chat/model/auditProbe.ts', source: 'import \'../providers/registry\'' },
  { label: 'model dynamic import', file: 'src/components/chat/model/auditProbe.ts', source: 'void import(\'../providers/registry\')' },
  { label: 'model computed dynamic import', file: 'src/components/chat/model/auditProbe.ts', source: 'void import(modulePath)' },
  { label: 'model require call', file: 'src/components/chat/model/auditProbe.ts', source: 'require(\'../results/tools\')' },
  { label: 'model import-equals declaration', file: 'src/components/chat/model/auditProbe.ts', source: 'import tools = require(\'../results/tools\')' },
  { label: 'model import type', file: 'src/components/chat/model/auditProbe.ts', source: 'type ProviderModule = typeof import(\'../providers/registry\')' },
  { label: 'provider side-effect import', file: 'src/components/chat/providers/auditProbe.ts', source: 'import \'../results/tools\'' },
  { label: 'provider dynamic import', file: 'src/components/chat/providers/auditProbe.ts', source: 'void import(\'../results/tools\')' },
  { label: 'provider require call', file: 'src/components/chat/providers/auditProbe.ts', source: 'require(\'../results/tools\')' },
  { label: 'provider computed require call', file: 'src/components/chat/providers/auditProbe.ts', source: 'require(modulePath)' },
  { label: 'provider import-equals declaration', file: 'src/components/chat/providers/auditProbe.ts', source: 'import tools = require(\'../results/tools\')' },
  { label: 'provider import type', file: 'src/components/chat/providers/auditProbe.ts', source: 'type ResultModule = typeof import(\'../results/tools\')' },
  { label: 'result type re-export', file: 'src/components/chat/results/auditProbe.ts', source: 'export type { Provider } from \'../providers/registry\'' },
  { label: 'result dynamic import', file: 'src/components/chat/results/auditProbe.ts', source: 'void import(\'../providers/registry\')' },
  { label: 'result require call', file: 'src/components/chat/results/auditProbe.ts', source: 'require(\'../providers/registry\')' },
  { label: 'result import-equals declaration', file: 'src/components/chat/results/auditProbe.ts', source: 'import registry = require(\'../providers/registry\')' },
  { label: 'result import type', file: 'src/components/chat/results/auditProbe.ts', source: 'type ProviderModule = typeof import(\'../providers/registry\')' },
]

const RESTRICTED_ASSERTION_SAMPLES: LintSample[] = [
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

const ALLOWED_ARCHITECTURE_SAMPLES: LintSample[] = [
  { label: 'model allowed dynamic diff import', file: 'src/components/chat/model/auditProbe.ts', source: 'void import(\'../diff/diffTypes\')' },
  { label: 'model allowed sibling import type', file: 'src/components/chat/model/auditProbe.ts', source: 'type ToolModule = typeof import(\'./toolCall\')' },
  { label: 'registry resolved content assertion', file: 'src/components/chat/providers/registry.ts', source: 'value as ResolvedMessageContent' },
  { label: 'checked builder tool call assertion', file: 'src/components/chat/model/createToolCall.ts', source: 'value as ToolCall' },
  { label: 'plugin imported related hook', file: 'src/components/chat/providers/probe/plugin.ts', source: 'import { related } from \'./spanRole\'\nconst plugin = { transcript: { relatedMessages: related } }' },
  { label: 'plugin imported hook factory', file: 'src/components/chat/providers/probe/plugin.ts', source: 'import { related } from \'./spanRole\'\nconst plugin = { transcript: { relatedMessages: related() } }' },
  { label: 'registration factory parameter hook', file: 'src/components/chat/providers/probe/registerProbeProvider.ts', source: 'export function registerProbeProvider(opts: { spanRole: () => string }) {\n  const plugin = { transcript: { spanRole: opts.spanRole } }\n  return plugin\n}' },
  { label: 'imported capability helper', file: 'src/components/chat/auditProbe.ts', source: 'import { agentTabSupportsInterrupt } from \'~/stores/tab.helpers\'\nvoid agentTabSupportsInterrupt(undefined)' },
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
  { label: 'aliased imported provider helper', file: 'src/components/chat/auditProbe.ts', source: 'import { AgentProvider } from \'~/generated/proto/leapmux/v1/agent_pb\'\nimport { isCodexProvider } from \'~/test-support/lintFixtures/providerDecision\'\nconst selected: AgentProvider = AgentProvider.CODEX\nconst matchesProvider = isCodexProvider\nvoid matchesProvider(selected)' },
]

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
 * Resolve `no-restricted-syntax` for each file, in a SUBPROCESS.
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
function inspectEslint(files: readonly string[], samples: readonly LintSample[]): {
  restrictedSyntax: Record<string, unknown>
  ruleIds: Record<string, Array<string | null>>
} {
  const script = `
    import { ESLint } from 'eslint'
    import { readFileSync } from 'node:fs'
    const eslint = new ESLint({ cwd: process.cwd() })
    const input = JSON.parse(readFileSync(0, 'utf8'))
    const restrictedSyntax = {}
    for (const file of process.argv.slice(1))
      restrictedSyntax[file] = (await eslint.calculateConfigForFile(file)).rules?.['no-restricted-syntax'] ?? null
    const ruleIds = {}
    for (const sample of input.samples) {
      const [result] = await eslint.lintText(sample.source, { filePath: sample.file })
      ruleIds[sample.label] = result.messages.map(message => message.ruleId)
    }
    process.stdout.write(JSON.stringify({ restrictedSyntax, ruleIds }))
  `
  const stdout = execFileSync(
    process.execPath,
    ['--input-type=module', '-e', script, ...files],
    {
      cwd: frontendRoot,
      encoding: 'utf8',
      input: JSON.stringify({ samples }),
      maxBuffer: 32 * 1024 * 1024,
    },
  )
  return JSON.parse(stdout) as {
    restrictedSyntax: Record<string, unknown>
    ruleIds: Record<string, Array<string | null>>
  }
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

describe('no-restricted-syntax keeps the base selectors', () => {
  let resolved: Record<string, unknown>
  let ruleIds: Record<string, Array<string | null>>
  let baseline: string[]

  // The timeout is explicit because the DEFAULT one does not fit the work.
  //
  // This hook boots a Node subprocess, loads `eslint.config.ts` through jiti,
  // and resolves three files against antfu's whole plugin tree. That measures
  // around ten seconds on a developer machine -- which is vitest's default hook
  // timeout exactly, so the suite passed or failed on machine load rather than
  // on anything about the config it guards.
  //
  // Sixty seconds is not a workaround for a slow test. The cost is inherent and
  // bounded: the subprocess is the point of the probe (see
  // resolveRestrictedSyntax), and the budget is sized so only a genuine hang
  // trips it.
  beforeAll(() => {
    const samples = [...RESTRICTED_IMPORT_SAMPLES, ...RESTRICTED_ASSERTION_SAMPLES, ...PROVIDER_DECISION_SAMPLES, ...REGISTRATION_SAMPLES, ...ALLOWED_ARCHITECTURE_SAMPLES]
    const inspection = inspectEslint([BASELINE_FILE, ...SCOPED_FILES], samples)
    resolved = inspection.restrictedSyntax
    ruleIds = inspection.ruleIds
    baseline = selectorsFor(resolved[BASELINE_FILE])
  }, 60_000)

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

  it.each(REGISTRATION_SAMPLES)('rejects an inline plugin hook: $label', ({ label }) => {
    expect(ruleIds[label] ?? []).toContain('chat-pipeline/plugin-registration-only')
  })

  it.each(ALLOWED_ARCHITECTURE_SAMPLES)('keeps the intentional exception: $label', ({ label }) => {
    expect((ruleIds[label] ?? []).filter(ruleId => ruleId !== null && ARCHITECTURE_RULE_IDS.has(ruleId))).toEqual([])
  })
})
