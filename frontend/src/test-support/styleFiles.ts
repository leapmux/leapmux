import { readdirSync } from 'node:fs'
import { join } from 'node:path'

/**
 * Every `.css.ts` file under `dir`, recursively.
 *
 * The enumeration two repo guards scan — `codeSurfacesArePaired.test.ts` (a
 * file that paints Shiki tokens must declare a code surface) and
 * `paletteColorsAreTokens.test.ts` (no stylesheet may hardcode a colour the
 * palette owns). Both exist to stop drift, and both carried a byte-identical
 * copy of this walker, so a change to what counts as a style file — a new
 * directory to skip, a second extension — would have moved one guard and left
 * the other scanning a different set.
 *
 * NOT a `.test.ts`, so vitest does not collect it as a suite of its own.
 */
export function collectStyleFiles(dir: string): string[] {
  const found: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory())
      found.push(...collectStyleFiles(full))
    else if (entry.name.endsWith('.css.ts'))
      found.push(full)
  }
  return found
}
