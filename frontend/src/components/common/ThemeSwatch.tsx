import type { ThemeVariant } from '~/styles/themes'
import { createMemo } from 'solid-js'
import { PipGrid } from '~/components/common/PipGrid'
import * as styles from './ThemeSwatch.css'

/**
 * The nine palette tokens the swatch shows, row-major.
 *
 * CHOSEN BY MEASUREMENT, not by taste, against two floors that every one of the
 * catalogue's variants has to clear. `ThemeSwatch.test.tsx` re-measures both on
 * all of them, so a new theme that breaks either one fails the suite.
 *
 * 1. No row and no column may hold two colours that look alike. The obvious
 *    role set -- the five semantic colours plus `--card`, `--muted`,
 *    `--secondary` and `--border` -- cannot clear this at any arrangement: the
 *    surface ramp tokens sit within 1 delta-E of each other, and `--card` and
 *    `--faint` are the SAME colour in one theme. This set's worst in-row or
 *    in-column pair is 12.1 delta-E.
 * 2. No pip may disappear into the background it is drawn on. That rules out
 *    the surface ramp again from the other side -- `--card` is 1.2 delta-E from
 *    its own background in Ayu Light. This set's worst is 9.4 delta-E.
 *
 * Two pairs here are the SAME colour in one variant, so the order below puts
 * each pair on a diagonal: `--primary` and `--warning` are `#ffcc66` and
 * `#ffcd66` in Ayu Mirage, and Default Dark states one value for both
 * `--border` and `--input`.
 *
 * `--ring` is absent because it equals `--primary` in all eleven themes, so it
 * would spend a pip to repeat one. No `*-foreground` token is here: a text
 * colour is near-black or near-white in every theme, so it says the same thing
 * about all of them. `--background` is the chip's fill rather than a pip, for
 * the same reason it cannot be both.
 */
export const SWATCH_TOKENS = [
  '--primary',
  '--accent',
  '--danger',
  '--success',
  '--warning',
  '--border',
  '--input',
  '--lm-icon-monochrome',
  '--lm-success-subtle',
] as const

/**
 * A theme's colours, as a chip: its background, with nine of its palette
 * tokens as a 3x3 block of pips.
 *
 * Decorative — the option's label carries the name. It exists because an
 * `<option>` never could: a palette is the one thing a picker cannot describe
 * in words, and it is the whole reason these two controls are menus rather than
 * native selects.
 */
export function ThemeSwatch(props: { variant: ThemeVariant }) {
  // Memoized because `PipGrid` reads `props.fills` once per pip: a plain
  // function would build the array nine times for every repaint, and hand back
  // a different one each time.
  //
  // `!` because every theme states every one of these tokens, which
  // `themes.test.ts` enforces for the whole token set and
  // `ThemeSwatch.test.tsx` re-checks for these nine by name.
  const fills = createMemo(() => SWATCH_TOKENS.map(token => props.variant.palette[token]!))

  return (
    <span
      class={styles.swatch}
      aria-hidden="true"
      data-testid="theme-swatch"
      // The border reads the LIVE theme's `--border`, not the previewed one, and
      // that is deliberate: it separates the chip from the menu behind it. A
      // dark palette previewed on a dark menu would have no edge at all if the
      // border came from the palette on show.
      style={{ 'background-color': props.variant.palette['--background']! }}
    >
      <PipGrid class={styles.swatchGrid} fills={fills()} />
    </span>
  )
}
