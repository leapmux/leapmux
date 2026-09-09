import { globalStyle } from '@vanilla-extract/css'

/**
 * Hide the browser's own scrollbar on one element, in BOTH engines.
 *
 * It takes two rules, and that is the whole reason this exists. Chromium
 * honours `scrollbar-width: none` and then ignores the pseudo-element; Safari
 * has only the pseudo-element. Six call sites wrote the pair by hand, so six
 * places could write half of one -- and half a pair is a bar that is invisible
 * on the engine the author tested and painted on the other.
 *
 * Neither rule stops the element SCROLLING. The wheel, the trackpad and the
 * keyboard reach the content exactly as before; only the painted bar goes.
 *
 * This emits the WEBKIT half only. Callers keep `scrollbarWidth: 'none'` in
 * their own `style()` block, where three of them already declare it among the
 * container's other properties -- moving those into a `globalStyle` would
 * change their position in the cascade against their own class rules, which no
 * unit test in this project can observe.
 *
 * `media` wraps the rule in a media query, for a caller that hides the bar only
 * on a touch pointer.
 *
 * Import this from a `.css.ts` file ONLY. It calls the vanilla-extract API in
 * the CALLER's file scope, which exists only while the compiler evaluates a
 * `.css.ts` module -- the same rule `declareAppLayers` in `~/styles/layers`
 * states, and for the same reason. Runtime code that imports it throws.
 */
export function hideNativeScrollbar(selector: string, media?: string): void {
  const hidden = { display: 'none' } as const
  globalStyle(
    `${selector}::-webkit-scrollbar`,
    media === undefined ? hidden : { '@media': { [media]: hidden } },
  )
}
