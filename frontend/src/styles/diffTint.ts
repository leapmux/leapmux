import type { ResolvedThemeMode } from './themes'

/**
 * How strongly a diff row is tinted, per polarity.
 *
 * A diff row mixes the code surface's own `--danger` / `--success` into the
 * surface at these strengths. Two things pull against each other and one number
 * cannot serve both polarities:
 *
 *   SEPARATION -- the row must read as changed against the plain surface.
 *   LEGIBILITY -- the code on it must stay as readable as the code around it.
 *
 * A dark surface moves further per unit of tint than a light one, so a single
 * strength gave dark MORE separation while costing it MORE contrast, and light
 * paid that cost out of a much thinner margin: light variants' own token
 * colours median 4.38:1 on their own background, where dark variants median
 * 7.40:1. At the flat 18% this replaces, a light diff row landed at 3.46:1 and
 * a light word-diff at 2.43:1.
 *
 * These strengths equalize BOTH measures across the two polarities -- the row
 * separates at 1.18 (light) and 1.19 (dark), and each keeps 84% and 83% of the
 * contrast its surface had untinted. The word-diff needs more on light for the
 * same reason the row needs less, landing both at 1.21 against the row beneath.
 *
 * These are a FLOOR on what the tint costs, not a ceiling on what it can be:
 * a light theme whose own tokens sit at 4.38:1 cannot reach 4.5:1 on a tinted
 * row however thin the tint, because the untinted surface does not reach it
 * either. That is the upstream palette's choice, and thinning the tint further
 * only makes a changed line harder to see. For reference, GitHub's own diff
 * rows separate at roughly 1.06 to 1.16 -- these stay above that.
 */
export const DIFF_TINT: Record<ResolvedThemeMode, { row: string, word: string }> = {
  light: { row: '14%', word: '18%' },
  dark: { row: '12%', word: '12%' },
}
