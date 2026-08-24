import { dirname, isAbsolute, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { collectStyleFiles } from '~/test-support/styleFiles'

// Three style guards read their whole verdict through this walk, and
// `focusRingsAreFocusVisible` already pins that it finds something. What no
// guard pins is the definition itself: a `.css.ts` and nothing else.

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')
const srcRoot = join(frontendRoot, 'src')

describe('collectStyleFiles', () => {
  it('returns an absolute path for every .css.ts under the root', () => {
    const found = collectStyleFiles(srcRoot)

    expect(found).toContain(join(srcRoot, 'components', 'common', 'ButtonGroup.css.ts'))
    expect(found.every(file => file.endsWith('.css.ts'))).toBe(true)
    expect(found.every(file => isAbsolute(file))).toBe(true)
  })

  it('rejects a plain .ts module', () => {
    expect(collectStyleFiles(srcRoot)).not.toContain(join(srcRoot, 'lib', 'agentProviders.ts'))
  })
})
