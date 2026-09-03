import { globalLayer } from '@vanilla-extract/css'

/**
 * Oat's own layers, in the order that the first line of `oat.min.css` declares
 * them: `@layer theme, base, components, animations, utilities;`.
 */
const OAT_LAYERS = ['theme', 'base', 'components', 'animations', 'utilities'] as const

/** The app's cascade layers, by role. */
export interface AppLayers {
  /**
   * Holds `controlReset` in `~/styles/shared.css.ts` -- the `all: unset` that
   * strips the UA and the Oat paint from a control.
   */
  reset: string
  /**
   * Holds the `[role^="menuitem"]` rules in `~/styles/global.css.ts`, which
   * replace Oat's own menu-item rule.
   */
  menuItem: string
}

/**
 * Declares every cascade layer that the app uses, IN THE CALLING `.css.ts`
 * FILE, and gives their names.
 *
 * Call it at the top of each `.css.ts` file that puts a rule in a layer, above
 * the rules themselves. Two properties of `@layer` make that the requirement.
 * A layer takes its position from the point where its NAME first appears, not
 * from where its rules are. And a statement appends only the names that the
 * document did not see yet, so a repeated statement is free.
 *
 * vanilla-extract guarantees the order of the rules inside one file only.
 * Across files the order is the order in which the bundler emits the
 * stylesheets, and that order changes with a chunking change, with the route
 * that loads first, and between the dev server and the build. A file that
 * declared one layer alone would therefore put its own layer BEFORE Oat's five
 * whenever the bundler emitted that file first, and Oat's `base` button rule
 * would then paint every chip as a solid primary pill. Each caller states the
 * whole order instead, so every interleaving of the stylesheets gives the same
 * result.
 *
 * The app's layers come last, after Oat's five. A rule in one of them beats
 * Oat's `base` paint, and loses to every unlayered class by construction. The
 * reset comes before the menu-item layer: it removes paint, and a rule that
 * paints on purpose belongs on top of it. No element carries both today.
 *
 * Import this from a `.css.ts` file ONLY. It calls the vanilla-extract API in
 * the CALLER's file scope, which exists only while the compiler evaluates a
 * `.css.ts` module. Runtime code that imports it throws.
 */
export function declareAppLayers(): AppLayers {
  for (const name of OAT_LAYERS)
    globalLayer(name)

  // Declared in this order, and read in this order: an object literal
  // evaluates its properties from top to bottom.
  return {
    reset: globalLayer('leapmuxReset'),
    menuItem: globalLayer('leapmuxMenuItem'),
  }
}
