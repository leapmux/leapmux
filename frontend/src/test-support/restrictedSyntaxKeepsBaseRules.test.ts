import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { ESLint } from 'eslint'
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

/** The DOM-`title` ban, which must survive beside the base selectors. */
const TITLE_SELECTOR = 'JSXAttribute[name.name="title"]'

/**
 * Every selector that `no-restricted-syntax` holds for one file.
 *
 * The rule takes each entry either as a bare selector string or as an object
 * with a `selector` field. Both forms appear in this config, so read both.
 */
function selectorsFor(config: Awaited<ReturnType<ESLint['calculateConfigForFile']>>): string[] {
  const entry = config.rules?.['no-restricted-syntax']
  if (!Array.isArray(entry))
    return []
  return entry
    .slice(1)
    .map(option => (typeof option === 'string' ? option : (option as { selector?: string }).selector))
    .filter((selector): selector is string => typeof selector === 'string')
}

describe('no-restricted-syntax keeps the base selectors', () => {
  let eslint: ESLint
  let baseline: string[]

  beforeAll(async () => {
    eslint = new ESLint({ cwd: frontendRoot })
    baseline = selectorsFor(await eslint.calculateConfigForFile(BASELINE_FILE))
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

  it.each(SCOPED_FILES)('keeps every baseline selector for %s', async (file) => {
    const scoped = selectorsFor(await eslint.calculateConfigForFile(file))
    const lost = baseline.filter(selector => !scoped.includes(selector))
    expect(
      lost,
      `The config block that sets no-restricted-syntax for ${file} dropped these `
      + 'selectors, because ESLint replaces rule options rather than merging them. '
      + `Spread ANTFU_RESTRICTED_SYNTAX into that block's options:\n  ${lost.join('\n  ')}`,
    ).toEqual([])
  })

  it.each(SCOPED_FILES)('still bans a DOM title for %s', async (file) => {
    const scoped = selectorsFor(await eslint.calculateConfigForFile(file))
    expect(
      scoped.some(selector => selector.includes(TITLE_SELECTOR)),
      `${file} must keep the DOM-\`title\` ban. Without it a bare \`title\` attribute `
      + 'renders the OS tooltip and becomes the accessible name of the element.',
    ).toBe(true)
  })
})
