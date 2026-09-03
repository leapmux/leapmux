import { readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { frontendRoot, posixRelative } from '~/test-support/sourceTree'
import { collectStyleFiles } from '~/test-support/styleFiles'

// A reset that another file composes must sit in a cascade LAYER.
//
// `all: unset` and the properties of the class that composes it tie on
// specificity -- one class each -- so the winner is whichever stylesheet the
// bundler emits last. vanilla-extract fixes the order of the rules inside one
// file only, and the order ACROSS files changes with a chunking change, with
// the route that loads first, and between the dev server and the build.
//
// `chipBase` lost that race. Its `all: unset` erased the `font-size`,
// `line-height` and `white-space` that `axisChip` declares for itself, and the
// composer's chips rendered at the ambient 16px, unclipped, in one browser and
// not in another. Nothing failed; the styles were simply gone.
//
// `controlReset` in `~/styles/shared.css.ts` is the layered reset, and it is
// the answer for every case here: a layered declaration always loses to an
// unlayered one, so a composing class keeps everything it declares, whatever
// the order.

const srcRoot = join(frontendRoot, 'src')

/** A named import from a vanilla-extract style module: `import { a, b } from '~/x.css'`. */
const STYLE_IMPORT = /import\s*\{([^}]*)\}\s*from\s*'([^']+\.css)'/g

/** The members a composition lists ahead of its own declarations: `style([a, b, {`. */
const COMPOSITION = /style\(\[([^\]{]*)/g

/** A reset written inline as the first member of a composition: `style([{ all: 'unset' }`. */
const INLINE_RESET = /style\(\[\s*\{\s*all:\s*'unset'/g

/** `all: 'unset'` written as a declaration. */
const BARE_RESET = /\ball:\s*'unset'/

/** The absolute path of the `.css.ts` file that `spec` names, or undefined. */
function resolveStyleModule(importer: string, spec: string): string | undefined {
  if (spec.startsWith('~/'))
    return join(srcRoot, `${spec.slice(2)}.ts`)
  if (spec.startsWith('.'))
    return resolve(dirname(importer), `${spec}.ts`)
  return undefined
}

/**
 * The source of `export const name = ...`, up to its balanced close.
 *
 * Returns an empty string when the file exports no such name, which happens
 * for an import this scan cannot follow (a re-export, a renamed import).
 */
function definitionOf(source: string, name: string): string {
  const start = source.search(new RegExp(`export const ${name}\\s*=`))
  if (start < 0)
    return ''
  let depth = 0
  for (let i = source.indexOf('(', start); i >= 0 && i < source.length; i++) {
    if (source[i] === '(')
      depth++
    else if (source[i] === ')' && --depth === 0)
      return source.slice(start, i + 1)
  }
  return source.slice(start)
}

/**
 * Every style that one file composes from another, as `[importer, definition]`.
 *
 * The scan reads the text, not the module graph, so it follows a plain named
 * import only. That covers every composition in the repo today, and the case
 * below fails if the count ever drops to zero.
 */
function crossFileCompositions(): { importer: string, owner: string, name: string, definition: string }[] {
  const found: { importer: string, owner: string, name: string, definition: string }[] = []
  for (const importer of collectStyleFiles(srcRoot)) {
    const source = readFileSync(importer, 'utf8')

    const owners = new Map<string, string>()
    for (const [, names, spec] of source.matchAll(STYLE_IMPORT)) {
      const owner = resolveStyleModule(importer, spec)
      if (!owner)
        continue
      for (const name of names.split(',').map(part => part.trim()).filter(Boolean))
        owners.set(name, owner)
    }

    for (const [, members] of source.matchAll(COMPOSITION)) {
      for (const member of members.split(',').map(part => part.trim())) {
        const owner = owners.get(member)
        if (!owner)
          continue
        found.push({
          importer,
          owner,
          name: member,
          definition: definitionOf(readFileSync(owner, 'utf8'), member),
        })
      }
    }
  }
  return found
}

describe('composed resets are layered', () => {
  it('never composes a style whose `all: unset` is unlayered', () => {
    const offenders = crossFileCompositions()
      // A definition that states a layer is the layered reset itself. No other
      // style in the repo puts `all: unset` and `@layer` in one definition, and
      // one that did would be layered for the same reason.
      .filter(({ definition }) => BARE_RESET.test(definition) && !definition.includes('\'@layer\''))
      .map(({ importer, owner, name }) =>
        `${posixRelative(srcRoot, importer)} composes ${name} from ${posixRelative(srcRoot, owner)}`)

    expect(
      offenders,
      'An unlayered `all: unset` erases the properties of the class that composes it, '
      + 'whenever the bundler emits the two files in the other order. Compose '
      + `\`controlReset\` from ~/styles/shared.css.ts instead:\n  ${offenders.join('\n  ')}`,
    ).toEqual([])
  })

  it('never writes a reset inline as a composed member', () => {
    // `style([{ all: 'unset' }, clippedText, {...}])` reads as if the reset were
    // ordered first. It is, inside this file -- and `clippedText` comes from
    // another one, where the order is not this file's to decide.
    const offenders: string[] = []
    for (const file of collectStyleFiles(srcRoot)) {
      for (const match of readFileSync(file, 'utf8').matchAll(INLINE_RESET))
        offenders.push(`${posixRelative(srcRoot, file)}: ${match[0]}`)
    }

    expect(
      offenders,
      `Compose \`controlReset\` from ~/styles/shared.css.ts instead:\n  ${offenders.join('\n  ')}`,
    ).toEqual([])
  })

  it('finds the compositions it is meant to be guarding', () => {
    // Without this the two cases above pass vacuously the day the import shape
    // or the composition shape changes, which is exactly when they must fail.
    expect(crossFileCompositions().length, 'no cross-file composition found -- has the layout moved?')
      .toBeGreaterThanOrEqual(10)
  })
})
