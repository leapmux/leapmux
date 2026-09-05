// Base CSS tokens are provided by @knadh/oat — see
// node_modules/@knadh/oat/css/01-theme.css for the full list of custom
// properties and their values. Common ones:
//   --space-{1..18}                spacing scale
//   --radius-{small,medium,large,full}
//   --text-{1..8}, --text-regular  font-size scale
//   --font-{normal,bold}           font-weight tokens (prefer these over
//                                  numeric weights; --font-medium and
//                                  --font-semibold also exist but our
//                                  convention is normal-or-bold only)
//   --leading-normal               default line-height
//   --shadow-{small,medium,large}
//   --transition, --transition-fast
//   --z-{dropdown,modal}

import type { ThemeVariant } from '~/styles/themes'
import { globalFontFace, globalStyle } from '@vanilla-extract/css'
import { DEFAULT_MONO_FONT_FAMILY } from '~/lib/fontStack'
import { blendedCodeTint, blendedTint, CODE_BLOCK_TINT_PERCENT, CODE_CARD_TINT_PERCENT, opaqueCodeTint } from '~/styles/codePalette'
import { DIFF_TINT } from '~/styles/diffTint'
import { declareAppLayers } from '~/styles/layers'
import { ALL_VARIANTS, DARK_VARIANTS, LIGHT_VARIANTS, resolveVariant } from '~/styles/themes'
import { defaultTheme } from '~/styles/themes/default'
import { breakpoints } from '~/styles/tokens'
import { darkVariantSelector, lightVariantSelector } from '~/styles/variantSelectors'

globalFontFace('Hack NF', {
  src: 'url("/fonts/HackNerdFont-3.003-Regular.woff2") format("woff2")',
  fontWeight: 400,
  fontStyle: 'normal',
  fontDisplay: 'swap',
})

globalFontFace('Hack NF', {
  src: 'url("/fonts/HackNerdFont-3.003-Bold.woff2") format("woff2")',
  fontWeight: 700,
  fontStyle: 'normal',
  fontDisplay: 'swap',
})

globalFontFace('Hack NF', {
  src: 'url("/fonts/HackNerdFont-3.003-Italic.woff2") format("woff2")',
  fontWeight: 400,
  fontStyle: 'italic',
  fontDisplay: 'swap',
})

globalFontFace('Hack NF', {
  src: 'url("/fonts/HackNerdFont-3.003-BoldItalic.woff2") format("woff2")',
  fontWeight: 700,
  fontStyle: 'italic',
  fontDisplay: 'swap',
})

globalStyle('html, body, #app', {
  height: '100%',
  width: '100%',
  overflow: 'hidden',
})

// iOS Safari viewport lock.
//
//  1. `position: fixed` + `height: var(--vvh, 100dvh)` ties the body to
//     the region the browser actually shows. `dvh` alone is NOT enough
//     on iOS: WebKit does not resize the layout viewport for the
//     on-screen keyboard — it moves part of the viewport out of sight
//     instead — so `dvh` still reports the full height with the keyboard
//     up, and the app runs on under it. `visualViewport.height`, which
//     `~/hooks/useVisualViewportInset` publishes as `--vvh` while the
//     keyboard is up, is the only value that accounts for the keyboard
//     there. `dvh` remains the fallback: it is correct for the browser
//     chrome, on a desktop, and on Chromium, where
//     `interactive-widget=resizes-content` (see `~/entry-server.tsx`)
//     makes the layout viewport itself shrink.
//  2. `padding-top: env(safe-area-inset-top)` keeps content out from
//     under the system status bar in standalone PWA mode. No bottom
//     padding — the composer sits flush with the screen bottom and the
//     home indicator overlays it translucently (KakaoTalk-style).
//  3. `transform: translateY(var(--vv-shift, 0px))` puts that
//     correctly-sized box in the right PLACE. `position: fixed` pins the
//     body to the layout viewport, so any displacement of the visual
//     viewport within it moves the app off the visible region — iOS
//     scrolls down to reveal the focused composer while the keyboard is
//     up, and iOS 26 leaves a residual offset after it dismisses
//     (FB19889436). Those run in opposite directions, so the hook
//     publishes `--vv-shift` already signed. `window.scrollTo(0, 0)`
//     can't fix either — body is `overflow: hidden`, there's nothing to
//     scroll.
//
// 1 AND 3 ARE ONE FIX FOR THE KEYBOARD-UP CASE. Applying the shift while
// the body is still `100dvh` runs away: the body is taller than the
// visible region, so moving it down pushes the composer below the
// keyboard, iOS scrolls further to reveal it, and the composer ends up
// permanently out of view, flashing in on each keystroke. Sizing without
// shifting leaves the app the right size in the wrong place, which is a
// gap under the composer. Together the body covers the visible region
// exactly, so iOS has nothing left to scroll into view.
//
// THE STRIP LEFT UNDER THE COMPOSER IN iOS SAFARI IS NOT OURS TO FILL.
// With the keyboard up, Safari keeps a band of its own chrome — the
// collapsed toolbar above the URL pill — between the page and the
// keyboard's accessory bar, and paints it in the page's background
// colour, so it reads as a gap in the app. Measured on iPhone at 3x with
// the keyboard up: `visualViewport` reports height 262 at offsetTop 452,
// which sums to `documentElement.clientHeight` (714) exactly, and a
// marker drawn on the body's bottom edge lands on a marker drawn at that
// 714 in layout coordinates. The body therefore already covers every row
// of page pixels that exists; the visual viewport IS the visible part of
// the page, and nothing can render past it. The standalone PWA has no
// such band because it has no Safari toolbar. Do not chase it with a
// taller `--vvh`: no value the platform reports describes that strip,
// and enlarging the body only pushes the composer under the keyboard.
//
// Also measured there, because it defeats the usual recipe: in iOS
// Safari `window.innerHeight` EQUALS `visualViewport.height` with the
// keyboard up (262 = 262), so the common
// `innerHeight - visualViewport.height` keyboard-height formula yields 0
// and cannot drive this layout. `documentElement.clientHeight` is the
// one that stays at the unshrunk 714.
//
// Note: the body's `transform` makes body the containing block for
// descendant `position: fixed` elements. Native HTML `popover` API
// consumers (DropdownMenu, Tooltip, GridSizePopover, the toast container)
// escape via the top layer. The one non-top-layered fixed consumer is
// `SelectionQuotePopover`, which counter-translates by
// `calc(-1 * var(--vv-shift, 0px))` to stay viewport-relative.
globalStyle('body', {
  position: 'fixed',
  top: 0,
  left: 0,
  width: '100%',
  height: 'var(--vvh, 100dvh)',
  paddingTop: 'env(safe-area-inset-top)',
  // NO padding-bottom: keep KakaoTalk-style intrusion (composer flush
  // with screen bottom, home-indicator translucently overlaying it).
  boxSizing: 'border-box',
  // iOS mitigation: cancel whichever way WebKit displaced the app from
  // the visible region (see note 3 above). Default 0 on every other
  // platform; the hook only sets it when it's actually non-zero.
  transform: 'translateY(var(--vv-shift, 0px))',
  willChange: 'transform',
})

// Kill iOS Safari's rubber-band overscroll on the page itself.
// `overscroll-behavior` alone isn't enough on iOS WebKit — the bounce
// is dispatched below that layer. `touch-action: none` on html+body
// refuses the pan gesture entirely at the page level.
//
// A nested scroll region still pans: the browser resolves a touch's
// allowed behaviour against the ancestors up to the element that handles
// the gesture, which for a touch inside the chat list is that list, not
// the page. So the message list needs no opt-in, and it sets none.
//
// The corollary is worth stating, because it is natural to assume the
// opposite: `touch-action` restricts ONLY the element that declares it,
// and a scroll container nested inside that element keeps both of its
// axes. Measured in Chromium and in WebKit, against a page built for the
// question. `pan-y` on a wrapper leaves a sideways-scrolling block
// inside it panning, `none` on a wrapper does the same, and so does the
// shape this app has -- a sideways block inside a vertical scroller
// inside the wrapper. Chromium also covers `none` on html+body, which
// leaves the list inside it panning, and `pan-x` on a wrapper, which
// leaves the list inside it scrolling down.
//
// The control is what makes those runs mean anything: the same value
// moved ONTO a scroller blocks that scroller and nothing else. A run
// whose control does not block is blind, not clean.
//
// The WebKit half was run by hand, and nothing guards it. Playwright
// reaches real touch through a Chromium-only protocol: its WebKit
// backend dispatches a tap with no move phase, and its Firefox backend
// does nothing at all.
//
// That is the rule for the whole app: a region declares `touch-action`
// only to constrain a gesture it owns, never to re-enable one. Three do.
// Two refuse a gesture — the scroll rail (`touchAction: 'none'`, see
// ~/components/chat/ChatScrollRail.css.ts), so a finger dragging its
// thumb scrubs instead of panning the list, and the tiling separator
// (see ~/components/shell/TilingLayout.css.ts), so a drag resizes the
// pane instead of scrolling. One narrows a gesture to the single axis it
// scrolls: the tab strip (`touchAction: 'pan-x'`, see
// ~/components/shell/TabBar.css.ts).
globalStyle('html, body', {
  overscrollBehavior: 'none',
  touchAction: 'none',
})

// Mobile form-control font-size floor. iOS Safari (browser + standalone
// PWA) auto-zooms when focusing an `<input>` / `<textarea>` / `<select>`
// whose computed font-size is < 16px. The zoom is NOT undone on blur
// and persists across in-app navigations (e.g. after submitting the
// login form), leaving the user on the next screen at ~1.15x scale with
// no easy way back. Anchoring form-control font-size at 16px on mobile
// is the standard suppression and does not affect desktop styling or
// disable user pinch-zoom.
globalStyle('input, textarea, select', {
  '@media': {
    [`(max-width: ${breakpoints.sm - 1}px)`]: {
      // iOS auto-zoom threshold; must be >= 16 CSS px. Hard-coded
      // because this is a WebKit-imposed value, not a design choice —
      // `rem` would scale with the user's root font-size and could fall
      // below the threshold, and Oat's `--text-*` tokens track design
      // intent for body text, which is a different concern.
      fontSize: '16px',
    },
  },
})

// The pre-hydration fallback. `~/lib/themeStore` writes `data-ui-theme` and
// `data-theme` onto <html> from an effect, so the very first paint has neither
// attribute; without this rule it would have no LeapMux palette at all and fall
// back to Oat's own colours.
//
// `html` (0,0,1), NOT `:root` (0,1,0). The variant loops below select on a bare
// `[data-ui-light="X"]`, which is ALSO (0,1,0), so a `:root` fallback tied them
// and only declaration order decided the winner -- the arrangement that let the
// code palette's twin of this rule outrank every variant and paint Default-light
// under all thirty. Lowering it makes the contest a specificity one, which no
// reordering of this module and no rule added in another `.css.ts` can revive.
// It still beats Oat's own `:root`, which sits inside `@layer theme`: an
// unlayered declaration wins over a layered one whatever the specificity.
globalStyle('html', {
  // Narrowed from Oat's `light dark`, and for the same reason the palette is
  // stated here: until the effect writes `data-theme`, the app IS light, and
  // `light-dark()` must not answer for a palette that is not showing.
  colorScheme: 'light',
  vars: {
    ...resolveVariant(defaultTheme, undefined, 'light').palette,

    // Typography — wire user-configurable fonts into Oat's variables. Not in
    // the theme modules: NOTICE.html has no preference store to read from, so
    // it declares its own literals instead of these indirections.
    '--font-sans': `var(--ui-font-family, system-ui, sans-serif)`,
    '--font-mono': `var(--mono-font-family, ${DEFAULT_MONO_FONT_FAMILY})`,
  },
})

// One rule per VARIANT: every light variant first, then every dark one.
//
// `data-theme` says which POLARITY is showing and nothing else, so which
// variant of that polarity is painted has to arrive separately. That is what
// `data-ui-light` / `data-ui-dark` on <html> carry -- the resolved variant for
// each half -- and it is why a theme can offer four flavours without adding a
// fourth `data-theme` value that every subtree selector would have to learn.
//
// The DESCENDANT half of each selector is what lets a subtree carry a different
// variant than the page. `TerminalView` renders `data-theme={terminalThemeMode()}`
// on its container so a dark terminal inside a light UI gets dark chrome, and
// before this that worked in one direction only: light used to be "the attribute
// is absent", so there was no `[data-theme="light"]` rule and a LIGHT terminal
// inside a dark UI still painted dark chrome. Both directions work now.
//
// The specificity is deliberate, not incidental. Every selector below except the
// bare `[data-ui-light="X"]` is (0,2,0), so:
//   - on one element, the dark rule wins over the light rule because it is
//     declared second, which is what <html> with both attributes needs;
//   - an element carrying the OPPOSITE `data-theme` matches only its own rule,
//     so a nested override wins on its own subtree regardless of order.
//
// KEEP THESE LOOPS AFTER THE `:root` BLOCK ABOVE. The bare `[data-ui-light="X"]`
// is (0,1,0), the same specificity as `:root`, so a non-default light palette
// beats the fallback by DECLARATION ORDER alone. Moving the fallback below them
// would leave every light variant painting the default palette, and only the
// dark variants would appear to work.
//
// KEEP THE LIGHT LOOP BEFORE THE DARK ONE, for the same reason: on <html>, which
// carries both attributes, the dark rule wins over the light one only because it
// is declared second.
//
// Do not scope these to `html` — that would raise the specificity of the
// self-match above the descendant match and silently kill subtree theming.
//
// `color-scheme` rides along with the palette, in the same rule, because it
// answers the same question and must never fall out of step with it. Oat sets
// `color-scheme: light dark` at :root, which leaves `light-dark()` resolving
// against the OS PREFERENCE -- and the app's polarity is its own choice, not
// the OS's. Three Oat components derive a colour that way from our tokens
// (skeleton, badge, alert), so on a dark app under a light OS the skeleton's
// shimmer took the light branch, `color-mix(in srgb, var(--muted) 15%, white)`:
// a near-white band at a median 9.2x the row's luminance, sweeping across it.
// It also tells the browser what to paint scrollbars and form controls as.
for (const variant of LIGHT_VARIANTS) {
  globalStyle(
    lightVariantSelector(variant),
    { colorScheme: 'light', vars: variant.palette },
  )
}
for (const variant of DARK_VARIANTS) {
  globalStyle(
    darkVariantSelector(variant),
    { colorScheme: 'dark', vars: variant.palette },
  )
}

// The CODE palette: the variant every highlighted surface wears.
//
// A code surface is the one place the app paints someone else's theme. The
// syntax preference can pin a palette AND a polarity of its own -- "code stays
// dark inside a light app" is a supported combination, not an accident -- and
// Shiki bakes its colours into each token span at tokenize time. Without a
// matching surface underneath, a dark theme's tokens land on the light page:
// measured across all 221 such combinations, the median token contrast falls
// from 7.13:1 to 1.97:1, and to 1.53:1 on a diff row.
//
// So `<html>` carries `data-code-variant` beside the four UI attributes, and
// these rules publish that variant's palette under a `--code-` prefix.
// `codeSurfaceTheme` (see ~/components/chat/shikiTokenColors.css.ts) points the
// app's own token names at these on the surface's subtree, so every existing
// `var(--danger)` inside a diff becomes the SYNTAX theme's red with no change
// at the call site.
//
// A PREFIXED SUBSET, not the whole palette. Naming the tokens states exactly
// what a code surface may re-theme; spreading all thirty would let a control
// that happens to sit inside one silently repaint itself.
//
// Two more are DERIVED from these rather than taken from the variant -- see the
// rule below the loop for `--code-block-background` and `--code-card`.
function codeVars(variant: ThemeVariant): Record<string, string> {
  return {
    '--code-background': variant.palette['--background']!,
    '--code-foreground': variant.palette['--foreground']!,
    '--code-border': variant.palette['--border']!,
    '--code-muted-foreground': variant.palette['--muted-foreground']!,
    '--code-faint-foreground': variant.palette['--faint-foreground']!,
    '--code-accent': variant.palette['--accent']!,
    '--code-danger': variant.palette['--danger']!,
    '--code-success': variant.palette['--success']!,
    // Keyed off the CODE variant's polarity, not the app's, so a dark syntax
    // theme inside a light app tints at the strength its own surface needs.
    '--code-diff-tint': DIFF_TINT[variant.polarity].row,
    '--code-diff-tint-word': DIFF_TINT[variant.polarity].word,
    '--code-color-scheme': variant.polarity,
  }
}

// The pre-hydration fallback for the CODE palette, the counterpart of the
// `:root` rule above.
//
// `data-code-variant` is written by an effect in `~/app.tsx`, so the first paint
// does not carry it -- and every `--code-*` token is published ONLY by the loop
// below. Until that effect runs, `codeSurfaceTheme`'s background and its whole
// `vars` remap are invalid at computed-value time: a diff row loses its tint (the
// surrounding `color-mix()` fails as a whole), the hunk separator loses its
// border, and a code block has no field of its own. Stating Default's light code
// palette here makes the first paint agree with the `:root` UI palette beside it,
// exactly as that rule's own comment argues.
//
// The pre-hydration fallback for the CODE palette.
//
// It exists because `data-code-variant` cannot be written at first paint: it is
// written only after `setSyntaxTheme` resolves, so the SURFACE never repaints
// ahead of the tokens that land on it (see `~/app.tsx`). Declared as `:root`
// after the loop, this fallback outranked every variant rule by declaration
// order alone -- `:root` is a pseudo-class at (0,1,0), the same specificity as
// `[data-code-variant="X"]`, and both match <html> -- so the code palette
// stayed Default-light under all thirty variants.
//
// BOTH POLARITIES, and the dark one is not decoration. `data-code-variant` is
// written only after `setSyntaxTheme` resolves, and for any of the 29 non-default
// variants that awaits a real `@shikijs/themes/*` chunk import -- while
// `~/lib/themeStore` writes `data-theme` synchronously at module import, so the
// app is ALREADY dark at first paint. A light-only fallback therefore painted
// every diff, Read view, tool body and fenced block in a dark app as a near-white
// slab for the whole round trip, then flipped.
//
// Default's own variant is the right answer for that window rather than a
// guess: the synchronous highlighter boots registered on Default's pair (see
// `~/lib/renderMarkdown`), so Default's colours ARE what the first tokens carry
// until the chosen pair loads. The two halves stay in step because both read
// the same catalogue entry.
// Both halves carry `:not([data-code-variant])`, which is what keeps them out of
// the variant rules' way. Specificity cannot do that job here: the dark half
// needs `[data-theme="dark"]` to pick its polarity, and `html[data-theme="dark"]`
// is (0,1,1) -- HIGHER than `[data-code-variant="X"]` at (0,1,0), so it would
// outrank every variant and reinstate the defect in the dark. A rule that stops
// MATCHING the moment the attribute lands needs no precedence argument at all,
// and it holds for whatever selector each half needs.
globalStyle('html:not([data-code-variant])', {
  vars: codeVars(resolveVariant(defaultTheme, undefined, 'light')),
})

globalStyle('html:not([data-code-variant])[data-theme="dark"]', {
  vars: codeVars(resolveVariant(defaultTheme, undefined, 'dark')),
})

for (const variant of ALL_VARIANTS) {
  globalStyle(`[data-code-variant="${variant.id}"]`, {
    vars: codeVars(variant),
  })
}

// The two fields a code surface paints, DERIVED from the palette above.
//
// One declaration answers for all thirty variants and for the fallback, because
// a custom property is substituted with the value the referencing element
// computes: `var(--code-foreground)` here resolves against whichever rule above
// won on <html>. Restating the pair in `codeVars` would copy the same expression
// thirty-one times.
//
//   --code-block-background  what a FENCED code block paints.
//   --code-card              a chip ON a code surface -- the copy button, the
//                            language label's hover.
//
// Every other code surface keeps `--code-background`. A diff and the Read view
// draw their rows inside an outline of their own, and tool output is text in the
// message flow with no padding to fill. Each surface states which it takes; see
// `CodeSurfaceKind` in ~/components/chat/shikiTokenColors.css.ts.
//
// TRANSLUCENT BY DEFAULT, so a block belongs to whatever hosts it -- the panel,
// an assistant band, or a user message's accent bubble. See `blendedCodeTint`.
globalStyle('html', {
  vars: {
    '--code-block-background': blendedCodeTint(CODE_BLOCK_TINT_PERCENT),
    '--code-card': blendedCodeTint(CODE_CARD_TINT_PERCENT),
  },
})

// ...and OPAQUE for the one case a tint cannot answer: a syntax theme pinned to
// the opposite polarity of the app.
//
// The two attributes this keys on are both written by the same effect in
// `~/app.tsx`, in that order, so the polarity can never describe a variant that
// is not showing. Absent -- before that effect first runs -- the tint applies,
// which is right for the overwhelmingly common case that the two agree.
//
// `html[a][b]` is (0,2,1) and the rule above is (0,0,1), so this wins on
// SPECIFICITY. Nothing here depends on which is declared first.
globalStyle(
  'html[data-theme="light"][data-code-polarity="dark"], html[data-theme="dark"][data-code-polarity="light"]',
  {
    vars: {
      '--code-block-background': opaqueCodeTint(CODE_BLOCK_TINT_PERCENT),
      '--code-card': opaqueCodeTint(CODE_CARD_TINT_PERCENT),
    },
  },
)

// EVERY code element wears the same step, and this is the one rule that paints
// them: inline `code` in a paragraph, a bare `<pre>` outside a code surface, and
// the three tags that mean the same thing in HTML and could arrive from a future
// renderer. It also overrides Oat's own `code, pre` fill (`var(--faint)`).
//
// The strength is the constant a code BLOCK's field is built from, so inline
// code and a fenced block are the same idea at the same weight, and neither can
// drift when the other is tuned -- including when it is retuned, which is why
// the 0.075 literal that used to sit here is gone. What differs is only what
// each one is a step FROM: see `blendedTint`, which reads `--foreground` and so
// answers with the SYNTAX theme's ink inside a code surface and the app's
// outside one.
//
// Only `code` and `pre` render today: the markdown chain has no sanitizer stage
// and `remarkRehype` drops raw HTML, so `kbd`, `samp` and `tt` cannot arrive
// from an agent, and no component writes them yet. They are named so the first
// one that appears is not the odd element out.
//
// NO BORDER, unlike a block. `codeBlockPre` outlines a block because a block has
// nothing but its colour to say where it begins; a run of inline code is bounded
// by the words around it, and a rule on it would disturb the line box it sits in.
const CODE_ELEMENTS = 'code, pre, kbd, samp, tt'

globalStyle(CODE_ELEMENTS, {
  backgroundColor: blendedTint(CODE_BLOCK_TINT_PERCENT),
})

// Oat rounds `code` and `pre` itself, at two different radii -- the inline
// corner and the block one. It says nothing about the other three, so they would
// wear a square field; this gives them the inline corner they belong with. NOT
// written into the rule above: that one is unlayered and Oat's is in its `base`
// layer, so naming `pre` here would override Oat's block radius with the inline
// one for every `<pre>` in the app.
globalStyle('kbd, samp, tt', {
  borderRadius: 'var(--radius-small)',
})

// Prevent a double step where they nest -- `pre > code` is every fenced block.
// `:is()` takes the specificity of its most specific argument, so this stays
// (0,0,2), exactly what the four hand-written pairs it replaced measured.
globalStyle(`:is(${CODE_ELEMENTS}) :is(${CODE_ELEMENTS})`, {
  backgroundColor: 'transparent',
})

// Reduce hr margin inside dropdown menus (Oat base sets var(--space-8) = 2rem).
globalStyle('ot-dropdown hr', {
  margin: 'var(--space-2) 0',
})

// Menu items are `<button role="menuitem">`, so Oat's base button rule
// (button.css: `background-color: var(--primary)`, a 1px border, medium radius)
// applies to them. Through 0.6.x Oat's own `[role="menuitem"]` rule cancelled
// that with `background: none; border: none; color: var(--foreground)`; 0.7
// narrowed that rule to layout only (display/width/padding/justify/cursor) and
// dropped the cancelling half, which rendered every menu item as a solid
// primary-coloured button.
//
// These declarations are ours now rather than something we inherit: a menu item
// needs them to read as a menu item at all, and leaning on a third-party
// internal reset is what made a patch-level bump able to restyle every menu in
// the app.
//
// The layout half (display, width, padding, justify-content) is restated here
// for the same reason. Oat's own rule is an EXACT `[role="menuitem"]` match, so
// a `menuitemcheckbox` or `menuitemradio` never received it and fell back to
// Oat's `base` button rule instead -- 16px of horizontal padding beside a
// sibling's 12px, and `inline-flex` with centred content, which in an unclassed
// block `<menu>` (the TabBar menus) shrinks the item to its text and paints the
// hover accent over the label alone. Cursor still comes from Oat.
//
// They live in a LAYER because Oat's rule did. Oat declares
// `@layer theme,base,components,animations,utilities` and put `[role=menuitem]`
// in `components`, so every unlayered vanilla-extract class outranked it no
// matter what the selectors were. Restating the rule unlayered would have
// silently changed that: a bare `[role="menuitem"]` is (0,1,0), the same
// specificity as a VE class, so `dangerMenuItem` and the two `menuItemSelected`
// classes would have been decided by stylesheet link order alone -- and a
// chunking change would have flipped "Delete…" back to the default colour and
// erased the selected-row highlight, with nothing to catch it.
// `declareAppLayers` states the whole order, Oat's five layers ahead of the
// app's own, so this layer appends past `utilities` whichever stylesheet the
// bundler emits first: it still beats Oat's `base` button fill, and still loses
// to every unlayered class by construction. Scoping the selector instead would
// NOT work -- it raises specificity and would beat those classes outright.
const { menuItem } = declareAppLayers()

// Prefix match, not an enumeration: it covers `menuitem`, `menuitemcheckbox`,
// and `menuitemradio`, and no other ARIA role begins with "menuitem". A list
// would silently drop a menu item's padding, alignment, and hover accent the
// first time an item takes the accurate checkbox or radio role.
globalStyle('[role^="menuitem"]', {
  '@layer': {
    [menuItem]: {
      display: 'flex',
      width: '100%',
      justifyContent: 'start',
      padding: 'var(--space-2) var(--space-3)',
      alignItems: 'center',
      gap: 'var(--space-2)',
      fontSize: 'var(--text-7)',
      textAlign: 'start',
      color: 'var(--foreground)',
      background: 'none',
      border: 'none',
      borderRadius: 'var(--radius-small)',
    },
  },
})

// Restated because the reset above cancels the background Oat's own hover rule
// would otherwise sit on top of, and this layer outranks Oat's `components`.
//
// `:not(:disabled, [aria-disabled="true"])` mirrors Oat's own
// `&:hover:not(:disabled)` on buttons. Menus here really do render disabled
// items -- BranchContextMenu's change and delete entries (the delete one is
// named after what it destroys, so its label reads "Delete worktree…" on a
// worktree row and "Delete branch…" on a main-repo row),
// FileActionsMenu's download entries while busy, OpenInAppButton's Refresh
// -- and Oat's `:disabled` rule sets only `cursor`/`opacity`, never
// `pointer-events: none`, so without the guard hovering one paints the accent
// that says "this will activate" on a control that will not.
globalStyle('[role^="menuitem"]:is(:hover, :focus):not(:disabled, [aria-disabled="true"])', {
  '@layer': {
    [menuItem]: {
      background: 'var(--accent)',
    },
  },
})

// Blockquotes: keep Oat's italic, but lift the color from the faint
// --muted-foreground to roughly halfway toward body text, so the quote reads
// clearly without being as loud as normal text.
globalStyle('blockquote', {
  color: 'color-mix(in oklab, var(--foreground), var(--muted-foreground))',
})

// Enable native width/height: auto transitions (progressive enhancement).
globalStyle(':root', {
  interpolateSize: 'allow-keywords',
} as any)

// Extend Oat button transitions to include color, border-color, and width.
globalStyle('button, [role="button"]', {
  'transition': 'background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), transform var(--transition-fast), width var(--transition-fast)',
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none',
    },
  },
})

// Consistent thin scrollbars across browsers (standard CSS — Firefox & Chrome 121+).
globalStyle('*', {
  scrollbarWidth: 'thin',
  scrollbarColor: 'var(--scrollbar-thumb) var(--scrollbar-track)',
})

// WebKit scrollbar styling (Safari & older Chrome).
globalStyle('*::-webkit-scrollbar', {
  width: '8px',
  height: '8px',
})

globalStyle('*::-webkit-scrollbar-track', {
  background: 'transparent',
})

globalStyle('*::-webkit-scrollbar-thumb', {
  backgroundColor: 'var(--scrollbar-thumb)',
  borderRadius: '4px',
  border: '2px solid transparent',
  backgroundClip: 'content-box',
})

globalStyle('*::-webkit-scrollbar-thumb:hover', {
  backgroundColor: 'var(--scrollbar-thumb-hover)',
})

globalStyle('*::-webkit-scrollbar-corner', {
  background: 'transparent',
})

// Prevent radio/checkbox inputs from shrinking inside flex containers.
globalStyle('input[type="radio"], input[type="checkbox"]', {
  flexShrink: 0,
})

// Render the focus ring inside the element so it is never clipped by an
// ancestor with overflow: hidden. Outline color and thickness still come
// from Oat's :focus-visible rule (2px solid var(--ring)); we only flip the
// offset from +2px (outside) to -2px (inside).
globalStyle(':focus-visible', {
  outlineOffset: '-2px',
})

// Add a 1px --background-colored ring just inside the focus outline on
// Oat-styled buttons so the teal outline stays distinguishable when the
// button itself is filled with --primary / --secondary / --danger. Uses
// var(--background) so the inner ring is invisible on surfaces that share
// the page background (where no separator is needed).
globalStyle(
  'button:focus-visible, [type="submit"]:focus-visible, [type="reset"]:focus-visible, [type="button"]:focus-visible, a.button:focus-visible, ::file-selector-button:focus-visible',
  {
    boxShadow: 'inset 0 0 0 2px var(--background)',
  },
)

// A popover that holds focus as a KEYBOARD RELAY draws no ring of its own.
//
// `DropdownMenu` gives its menu popover `tabindex="-1"` and focuses it on open,
// because `popover="auto"` moves focus nowhere: without that the trigger keeps
// focus and every arrow key and typed character goes to the document instead of
// the list. The popover is therefore a focus HOST, never a stop the user
// navigated to -- `tabindex="-1"` keeps it out of the Tab order entirely.
//
// Whether a ring appears on it was left to each engine's `:focus-visible`
// heuristic, and the engines disagree. Chromium suppresses it after a click;
// the WKWebView the macOS desktop app runs on paints it, and not on every open,
// so the same click produced a ring around the whole menu on one platform and
// nothing on the other. A heuristic is the wrong thing to depend on here in
// either direction: this element is not a control, so the answer is the same
// for a mouse and for a keyboard.
//
// Feedback for the keyboard user is not lost. The first Arrow key moves focus
// to a menu ITEM, which rings normally -- that is the element the user is
// actually navigating, and a 2px outline around the panel only repeated the
// border the panel already draws.
//
// `[tabindex="-1"]` is what limits this to the relay case. A `card` popover
// takes no tabindex, because its own controls Tab in the ordinary way, and
// each of those keeps its ring.
globalStyle('[popover][tabindex="-1"]:focus, [popover][tabindex="-1"]:focus-visible', {
  outline: 'none',
})
