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
const CHAT_SCOPED_FILES: Array<{ file: string, marker: string }> = [
  // The provider-neutrality block covers shared modules outside the plugin layer.
  { file: 'src/stores/chatTypes.ts', marker: 'TSTypeReference[typeName.name=\'Record\'] TSTypeReference[typeName.name=\'AgentProvider\']' },
  // The chat-wide block adds the tool-call assertion ban.
  { file: 'src/components/chat/results/ToolMessage.tsx', marker: 'TSAsExpression[typeAnnotation.typeName.name=/^(ToolCallIR' },
  // The IR blocks add the value-import boundary, one selector per depth.
  { file: 'src/components/chat/ir/toolCall.ts', marker: 'source.value=/^~\\/(?!lib' },
  { file: 'src/components/chat/ir/tools/generic.ts', marker: 'source.value=/^\\.\\.\\/(?![^/]+$' },
  // The provider JSX ban reaches a non-control `.tsx` but not the control surfaces.
  { file: 'src/components/chat/providers/claude/extractors/toolCall.ts', marker: 'TSAsExpression[typeAnnotation.typeName.name=/^(ToolCallIR' },
  { file: 'src/components/chat/providers/pi/PiControlActions.tsx', marker: 'TSAsExpression[typeAnnotation.typeName.name=/^(ToolCallIR' },
]

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
function resolveRestrictedSyntax(files: readonly string[]): Record<string, unknown> {
  const script = `
    import { ESLint } from 'eslint'
    const eslint = new ESLint({ cwd: process.cwd() })
    const out = {}
    for (const file of process.argv.slice(1))
      out[file] = (await eslint.calculateConfigForFile(file)).rules?.['no-restricted-syntax'] ?? null
    process.stdout.write(JSON.stringify(out))
  `
  const stdout = execFileSync(
    process.execPath,
    ['--input-type=module', '-e', script, ...files],
    { cwd: frontendRoot, encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 },
  )
  return JSON.parse(stdout) as Record<string, unknown>
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
    const chatFiles = CHAT_SCOPED_FILES.map(entry => entry.file)
    resolved = resolveRestrictedSyntax([BASELINE_FILE, ...SCOPED_FILES, ...chatFiles])
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

  // A chat-scoped block that stops matching its tree leaves its architecture
  // selectors silently gone -- the same failure the baseline check above reads,
  // one tree further in. Each marker below is one selector the block for that
  // tree must resolve with.
  it.each(CHAT_SCOPED_FILES)('keeps the chat architecture selectors for %s', ({ file, marker }) => {
    const scoped = selectorsFor(resolved[file])
    expect(
      scoped.some(selector => selector.includes(marker)),
      `${file} lost the chat pipeline selector that starts \`${marker}\`. The scoped `
      + 'block in `eslint.config.ts` no longer matches this tree, so the rule guards nothing.',
    ).toBe(true)
  })

  // The four control surfaces keep JSX -- and a control file resolving WITHOUT
  // the JSX ban pins the exemption from the other side: the ban block must not
  // swallow them, or every permission prompt in the app fails lint.
  it('keeps the provider JSX ban off the control surfaces', () => {
    const scoped = selectorsFor(resolved['src/components/chat/providers/pi/PiControlActions.tsx'])
    expect(
      scoped.some(selector => /^JSXElement$|^JSXFragment$/.test(selector)),
      'A control surface must keep its JSX: the ban is for transcript rows, and this file answers a request.',
    ).toBe(false)
    expect(scoped.some(selector => selector.includes(TITLE_SELECTOR))).toBe(true)
  })
})
