// How a code surface derives the two fields it paints from the syntax variant it
// wears, and the two forms each of them takes.
//
// The values live here, and not in ~/styles/global.css.ts, because a test has
// to read them: `codePalette.test.ts` measures the derived fields against every
// catalogued variant, and a `.css.ts` module cannot be imported outside the
// vanilla-extract compiler. Keeping the percentages in one plain module is what
// stops the guard from measuring a strength the stylesheet no longer uses.
//
// WHY A DERIVATION AND NOT TWO MORE PALETTE TOKENS. A syntax variant states one
// background and one foreground. Everything a code surface needs above that
// background is a step toward its foreground, so a step is what these express --
// eleven themes and thirty variants get the two fields for free, and a theme
// added later cannot forget to state them.
//
// TWO FORMS, ONE STEP. The step is the same in both; what differs is what it is
// a step FROM. See `blendedCodeTint` and `opaqueCodeTint`.

/**
 * How far a code element's field sits from the surface under it, in percent.
 *
 * Every code element takes this one step -- a fenced block, and inline `code` in
 * a paragraph -- so the two are the same idea at the same weight and neither can
 * drift when the other is tuned.
 *
 * A block is recessed on a light palette and raised on a dark one, because the
 * step is toward the foreground and the foreground is dark on one and light on
 * the other. Deliberately LIGHTER than the 7.5% the app used before the syntax
 * theme existed, which read as too dark against a light palette and too bright
 * against a dark one -- and this step is now the ONLY thing that marks a block,
 * because the block carries no border. It holds 1.07:1 against the tightest host
 * in the catalogue (Everforest light, inside a user message's accent bubble),
 * which is a quiet block by design.
 *
 * The ceiling is the code sitting ON it: at 7% the worst case falls to 3.98:1,
 * because several palettes give their accent bubble only 4.51-5.15:1 to begin
 * with and a recessed field spends from that. 6% keeps it at 4.05:1.
 */
export const CODE_BLOCK_TINT_PERCENT = 6

/**
 * The same, for a chip that sits ON a code surface -- the copy button and the
 * language label.
 *
 * Twice the block's own step, so the chip reads as raised whether it lands on a
 * block or on a flat code page. It used to be the variant's own `--card`, which
 * measured 1.003:1 against the block field on Nord's dark variant: a hover state
 * that reported nothing.
 */
export const CODE_CARD_TINT_PERCENT = 15

/**
 * How far a border INSIDE a code surface sits from the field behind it, in
 * percent.
 *
 * A code block itself is unbordered -- its field is what marks it. What this
 * serves is the chrome that sits ON that field: the copy button carries an
 * outline, and against a field that takes its host's hue the code page's own
 * `--border` measured 1.0005:1 on Ayu's light variant inside an accent bubble --
 * an outline that was not there. Stepping from the field instead holds it at
 * 1.24:1 or better, on every palette and in both field forms.
 *
 * THE WEIGHT EVERY OTHER BORDER IN THE APP CARRIES. Each palette's own
 * `--border` is a 15.5%-23.5% step toward its foreground, mean 18.9%, so this
 * chrome is outlined like the rest of the UI rather than more heavily than it.
 */
export const CODE_BORDER_TINT_PERCENT = 18

/**
 * The step as a TRANSLUCENT tint, which composites onto whatever is behind it.
 *
 * THE DEFAULT FORM, because a code block does not choose what it lands on. The
 * app paints three surfaces under a message body -- the panel's `--background`,
 * an assistant band's `--card`, and a user message's `--accent` -- and an opaque
 * field can only match one of them. Against the accent bubble the opaque form
 * measured as little as 1.051:1 while sitting up to 68.9 sRGB units away from
 * it: too flat to read as a deliberate step, and too far to belong to the
 * bubble. A tint takes the host's own hue and lands one constant step off it,
 * whatever the host is, and it needs no host to declare anything.
 *
 * Every token that lands ON a blended field takes this form too -- the chip and
 * the border. An opaque chip on a blended field disappears (1.000:1 on One's
 * light variant over a band) because the two are then measured from different
 * bases. A blended one steps from what it actually sits on, which is why the
 * border keeps working in the opaque case below as well.
 */
export function blendedCodeTint(percent: number): string {
  return `rgb(from var(--code-foreground) r g b / ${percent}%)`
}

/**
 * The same step, read from `--foreground` instead.
 *
 * THE FORM TO WRITE IN A RULE, and it is the same colour as
 * {@link blendedCodeTint} wherever it matters. `codeSurfaceTheme` repoints
 * `--foreground` at `--code-foreground` on every code surface, so inside one
 * this resolves to the syntax theme's ink; outside one -- inline `code` in a
 * paragraph, a `<kbd>` in a menu -- it resolves to the app's, which is the ink
 * that prose is set in and the right base for a chip that sits in it.
 *
 * {@link blendedCodeTint} exists only for the tokens declared on `<html>`, where
 * no surface has repointed anything yet and `--foreground` is still the app's.
 */
export function blendedTint(percent: number): string {
  return `rgb(from var(--foreground) r g b / ${percent}%)`
}

/**
 * The step as an OPAQUE mix over the syntax theme's own background.
 *
 * For the one case the tint cannot answer: a syntax theme pinned to the OPPOSITE
 * polarity of the app. Shiki bakes each token's colour at tokenize time, so a
 * dark theme's tokens are light -- and a translucent tint over a light page
 * stays a light field, which put those tokens at a median 1.97:1. Mixing against
 * the code background instead carries the field across the flip with them.
 *
 * The block is SUPPOSED to look foreign here. It is wearing another theme, and
 * `codeBlockPre`'s border is what still reads it as a block.
 */
export function opaqueCodeTint(percent: number): string {
  return `color-mix(in srgb, var(--code-foreground) ${percent}%, var(--code-background))`
}
