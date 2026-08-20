import { globalStyle } from '@vanilla-extract/css'
import { blendedTint, CODE_BORDER_TINT_PERCENT } from '~/styles/codePalette'

/**
 * Which of the code palette's two fields a surface paints.
 *
 *   - `page` -- the syntax theme's own background, `--code-background`. What a
 *     surface wants when it is already delimited by something other than its
 *     colour, or when it carries no padding to fill: the diff and the Read view
 *     draw their own rows inside their own outline, the raw-JSON body has a
 *     dashed one, and tool output is mono text in the message flow, where a
 *     field hugging bare glyphs reads as a highlighter pen.
 *   - `block` -- `--code-block-background`, a step off that page. What a FENCED
 *     code block wants: it has nothing but its colour to say where it starts,
 *     and the page it sits on is the same colour whenever the syntax theme and
 *     the UI theme resolve to the same palette -- which is the default, and is
 *     when the block used to disappear entirely.
 *
 * Stated at every call site rather than defaulted, because a surface that takes
 * the wrong one is wrong in a way that only shows on some themes.
 */
export type CodeSurfaceKind = 'block' | 'page'

/**
 * Make `selector` a CODE SURFACE: an element that wears the syntax theme's
 * palette instead of the app's.
 *
 * The sibling of {@link shikiDualThemeColors}. That one paints the tokens; this
 * one paints what they land on -- and they must be applied to the same surfaces
 * or the two disagree. Shiki bakes each token's colour in at tokenize time, so
 * a syntax theme pinned to the opposite polarity of the app puts dark tokens on
 * a light page unless the surface answers for itself.
 *
 * It works by REPOINTING the app's own token names at the `--code-` prefixed
 * ones `<html>` publishes (see the `data-code-variant` loop in
 * ~/styles/global.css.ts). Custom properties inherit, so every rule inside the
 * subtree -- `var(--danger)` on a removed diff row, `var(--faint-foreground)`
 * on a line number, `var(--border)` on the container -- resolves to the syntax
 * theme's value with nothing to change at the call site. That is the whole
 * point: a new rule inside a code surface is themed correctly by default, and
 * cannot be forgotten.
 *
 * `--background` is repointed at the field this surface actually paints, not at
 * the code page, so a child that fills "the background" stays flush with the
 * code around it. The hunk separator between two diff hunks is that child.
 *
 * The field is OPAQUE either way. A translucent tint cannot survive a polarity
 * flip: painted at a percentage of the foreground it blends onto whatever it
 * lands on, which on a light page stays a light field no matter what the syntax
 * theme is.
 */
function codeSurfaceTheme(selector: string, kind: CodeSurfaceKind): void {
  const block = kind === 'block'
  globalStyle(selector, {
    backgroundColor: block ? 'var(--code-block-background)' : 'var(--code-background)',
    color: 'var(--code-foreground)',
    // So `light-dark()` and any UA-painted widget inside the surface answer to
    // the SYNTAX variant's polarity, not the page's.
    colorScheme: 'var(--code-color-scheme)',
    vars: {
      // `transparent` on a BLOCK, not the field again. A block's field is
      // normally a translucent tint, so a child that fills "the background"
      // would composite the tint a second time and paint itself a step darker
      // than the block it sits in. Showing the block through is what "the
      // background" means inside one. A page surface can name its own field,
      // because that one is opaque -- the diff's hunk separator does.
      '--background': block ? 'transparent' : 'var(--code-background)',
      '--foreground': 'var(--code-foreground)',
      '--card': 'var(--code-card)',
      // Inside a BLOCK, a border steps from the field behind it rather than
      // from the code page. The block itself is unbordered, but the chrome on it
      // is not -- the copy button carries an outline -- and the field takes the
      // colour of whatever hosts it, so the code page's own `--border` measured
      // 1.0005:1 against a block inside an accent bubble on Ayu's light variant:
      // an outline that was not there. A blended step holds at 1.24:1 on every
      // palette and in both field forms, at the same weight every other border
      // in the app carries. A page surface keeps `--code-border`, which its own
      // opaque field can be measured against.
      '--border': block ? blendedTint(CODE_BORDER_TINT_PERCENT) : 'var(--code-border)',
      '--muted-foreground': 'var(--code-muted-foreground)',
      '--faint-foreground': 'var(--code-faint-foreground)',
      '--accent': 'var(--code-accent)',
      '--danger': 'var(--code-danger)',
      '--success': 'var(--code-success)',
    },
  })
}

/**
 * Emit the shiki dual-theme color contract for `selector`: the light-theme color
 * in the base rule and the dark-theme color under `html[data-theme="dark"]`.
 *
 * Single-sources the `--shiki-light` / `--shiki-dark` (+ optional `*-bg`) variable
 * pair that Shiki output (`pre.shiki span`, `span[data-shiki-token]`, the inline
 * `span[style]` decorations) keys off across every highlighted surface -- the token
 * worker, the markdown pipeline, the editor parser, the ANSI renderer, and the Read
 * tool view all emit it via dualThemeTokenOptions. Routing every wrapper through
 * this helper keeps a variable rename from silently missing one of the ~dozen
 * selectors that style the same contract.
 *
 * `bg` also maps the `--shiki-*-bg` background variables (the token spans whose
 * wrapper owns the block background). Diff/markdown surfaces that must NOT override
 * an inline word-diff/background with a higher-specificity global omit it.
 */
function shikiDualThemeColors(selector: string, opts?: { bg?: boolean }): void {
  globalStyle(selector, {
    color: 'var(--shiki-light)',
    ...(opts?.bg ? { backgroundColor: 'var(--shiki-light-bg, transparent)' } : {}),
  })
  globalStyle(`html[data-theme="dark"] ${selector}`, {
    color: 'var(--shiki-dark)',
    ...(opts?.bg ? { backgroundColor: 'var(--shiki-dark-bg, transparent)' } : {}),
  })
}

/**
 * Declare a highlighted code surface: its own palette, and the token rules that
 * land ON it.
 *
 * THE ONE DOOR, and the two halves are no longer reachable apart. A surface
 * needs both -- `codeSurfaceTheme` repoints the app's token names at the syntax
 * theme's `--code-*`, and `shikiDualThemeColors` emits the `--shiki-light` /
 * `--shiki-dark` contract the token spans key off -- and a surface that took one
 * without the other put dark tokens on a light page at a median 1.97:1. The
 * repo guard that watched for that could only scan a FILE for both names: two
 * calls on DIFFERENT selectors passed it, because a text scan cannot see a
 * selector.
 *
 * Each token selector is DERIVED from `surface` by suffix, so the pair cannot
 * land on different elements by construction. A suffix that starts with a class
 * or an attribute rather than a space narrows the SAME element -- which is how a
 * markdown body makes every `<pre>` a surface while colouring only the ones
 * Shiki reached (`pre` + `.shiki`), so a block looks the same before and after
 * the highlighted render swaps in.
 *
 * `kind` says which field the surface paints; see {@link CodeSurfaceKind}.
 *
 * `bg` maps the `--shiki-*-bg` background variables too. A diff or markdown
 * surface that must not override an inline word-diff background with a
 * higher-specificity global omits it.
 */
export function codeSurface(surface: string, kind: CodeSurfaceKind, tokens: { suffix: string, bg?: boolean }[]): void {
  codeSurfaceTheme(surface, kind)
  for (const token of tokens)
    shikiDualThemeColors(`${surface}${token.suffix}`, { bg: token.bg })
}
