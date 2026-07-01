import themeGithubDark from '@shikijs/themes/github-dark'
import themeGithubLight from '@shikijs/themes/github-light'

/**
 * The two GitHub themes every Shiki highlighter in the app registers, with their
 * background forced transparent so the app's own wrapper owns the block background.
 *
 * This lives in its own lean module -- deliberately free of grammar and
 * markdown-pipeline imports -- so the engine-level `shikiLazyHighlighter` (and
 * therefore the token worker, the markdown worker, and the editor that import it) can
 * source the themes WITHOUT transitively pulling in `markdownProcessor`'s eager
 * 20-grammar set and the full remark/rehype/unified pipeline.
 */
export const transparentBgThemes = [themeGithubLight, themeGithubDark].map(t => ({ ...t, bg: 'transparent' }))

/**
 * The dual-theme `codeToTokens`/`codeToHast`/`codeToHtml` options every Shiki call
 * site in the app shares: the `github-light`/`github-dark` pair with `defaultColor`
 * off, so Shiki emits per-token `--shiki-light`/`--shiki-dark` CSS variables instead
 * of a single baked-in color.
 *
 * This contract is load-bearing: the CSS that themes Shiki output (the `pre.shiki` /
 * `[data-shiki-token]` rules in messageStyles / toolStyles / markdownContent) keys
 * off exactly these variables. Single-sourcing it here keeps the theme names and
 * `defaultColor` flag from drifting between the token worker, the markdown pipeline,
 * the editor parser, the ANSI renderer, and the Read tool view -- a mismatch in one
 * path would silently theme that surface differently.
 */
export const DUAL_THEME_TOKEN_OPTIONS = {
  themes: { light: 'github-light', dark: 'github-dark' },
  defaultColor: false,
} as const
