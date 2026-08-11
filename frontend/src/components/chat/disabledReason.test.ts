import { readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// The composer states why it is dead on several surfaces: the box's own
// placeholder, the [+] menu's attach item, and every settings submenu. They used
// to disagree -- the menu and the option popover each carried their own
// hardcoded sentence, so a subagent tab read "subagent" inside the box and
// "agent" in the menu, within one glance.
//
// The fix is ONE resolution: TileRenderer resolves the reason, AgentEditorPanel
// threads the resolved string down, and its PRESENCE is what disables the
// composer -- so "dead with nothing to say" is unrepresentable in the types.
//
// What the types cannot catch is a leaf that invents a SECOND wording for the
// same condition. That is invisible to a type check and to every render test,
// because each surface renders a plausible sentence until you open two at once.
// So this guards the property that actually decays: only the owner names the
// condition, and no composer surface writes its own.

const chatRoot = dirname(fileURLToPath(import.meta.url))
const frontendSrc = resolve(chatRoot, '..', '..')

/**
 * Phrasings of "this composer takes no input". The real drift used the first;
 * the others are the near-misses a reader would reach for next.
 */
const DISABLED_PHRASINGS = [
  'does not accept',
  'doesn\'t accept',
  'accepts no input',
  'not accepting',
]

/** The one module that owns the sentence, plus this guard. */
const OWNERS = [
  join(frontendSrc, 'components', 'shell', 'TileRenderer.tsx'),
  fileURLToPath(import.meta.url),
]

/**
 * The composer surfaces. Each renders the reason; none may author one. Scoped to
 * these directories rather than the whole tree so the guard stays about the
 * composer and does not trip over unrelated prose elsewhere.
 */
const COMPOSER_DIRS = [
  join(chatRoot, 'composer'),
  join(chatRoot, 'markdownEditor'),
  join(frontendSrc, 'lib', 'editor'),
]

describe('the disabled-composer reason', () => {
  it('is authored by exactly one module', async () => {
    const { globSync } = await import('node:fs')
    const offenders: string[] = []
    for (const dir of COMPOSER_DIRS) {
      for (const rel of globSync('**/*.{ts,tsx}', { cwd: dir })) {
        const path = join(dir, rel)
        if (OWNERS.includes(path) || path.endsWith('.test.ts') || path.endsWith('.test.tsx'))
          continue
        // Strip comments first: a comment naming the condition is how the
        // resolved reason is DOCUMENTED as it travels, and only code that spells
        // the sentence out is a second wording.
        const code = readFileSync(path, 'utf8')
          .replace(/\/\*[\s\S]*?\*\//g, '')
          .replace(/\/\/[^\n]*/g, '')
        if (DISABLED_PHRASINGS.some(phrase => code.includes(phrase)))
          offenders.push(path)
      }
    }
    expect(offenders).toEqual([])
  })

  it('is stated by the owner', () => {
    // Read as source rather than imported: the constant is module-private, and
    // the point is that the sentence lives in exactly one file.
    const owner = readFileSync(OWNERS[0], 'utf8')
    expect(owner).toContain('SUBAGENT_NO_MESSAGES_HINT')
    expect(owner).toMatch(/This subagent .*accept messages\./)
  })
})
