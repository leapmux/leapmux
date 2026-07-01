import { globalStyle } from '@vanilla-extract/css'

/**
 * Emit the shiki dual-theme color contract for `selector`: the light-theme color
 * in the base rule and the dark-theme color under `html[data-theme="dark"]`.
 *
 * Single-sources the `--shiki-light` / `--shiki-dark` (+ optional `*-bg`) variable
 * pair that Shiki output (`pre.shiki span`, `span[data-shiki-token]`, the inline
 * `span[style]` decorations) keys off across every highlighted surface -- the token
 * worker, the markdown pipeline, the editor parser, the ANSI renderer, and the Read
 * tool view all emit it via DUAL_THEME_TOKEN_OPTIONS. Routing every wrapper through
 * this helper keeps a variable rename from silently missing one of the ~dozen
 * selectors that style the same contract.
 *
 * `bg` also maps the `--shiki-*-bg` background variables (the token spans whose
 * wrapper owns the block background). Diff/markdown surfaces that must NOT override
 * an inline word-diff/background with a higher-specificity global omit it.
 */
export function shikiDualThemeColors(selector: string, opts?: { bg?: boolean }): void {
  globalStyle(selector, {
    color: 'var(--shiki-light)',
    ...(opts?.bg ? { backgroundColor: 'var(--shiki-light-bg, transparent)' } : {}),
  })
  globalStyle(`html[data-theme="dark"] ${selector}`, {
    color: 'var(--shiki-dark)',
    ...(opts?.bg ? { backgroundColor: 'var(--shiki-dark-bg, transparent)' } : {}),
  })
}
