import type { StyleRule } from '@vanilla-extract/css'

/**
 * The fade over the last line of a box that clips its content.
 *
 * A plain `.ts` module rather than a `.css.ts` one, because vanilla-extract
 * lets a `.css.ts` file export only plain data -- a function export fails the
 * whole module at build time with `Invalid exports`. `~/styles/codeBlock.ts`
 * holds the shared code-surface rules for the same reason.
 *
 * Both spellings, always. The unprefixed `mask-image` and the `-webkit-` one
 * are a PAIR: WebKit needs the prefixed property, and a site that edits one line
 * and not the other loses the fade in exactly one engine, which no test catches.
 * Returning the two together makes that impossible.
 *
 * `fadeEm` is how tall the fade is, in the box's own line-height units, so a
 * caller states it as the number of lines to dim.
 *
 * Usage in a `.css.ts` file:
 * ```ts
 * export const collapsed = style({ maxHeight: '3.6rem', overflow: 'hidden', ...fadeMaskBottom() })
 * ```
 */
export function fadeMaskBottom(fadeEm = 1.5): StyleRule {
  const mask = `linear-gradient(to bottom, black calc(100% - ${fadeEm}em), transparent)`
  return { WebkitMaskImage: mask, maskImage: mask }
}
