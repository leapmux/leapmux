// The popover-card inset, as plain data.
//
// Two consumers need this value and only one of them can execute CSS: `~/styles/popover.css.ts`
// declares it as `popoverCardPadding`, and `tests/e2e/036-dropdown-popover.spec.ts` measures the
// resolved pixels through a page-side probe. Playwright runs no Vite and no vanilla-extract, so a
// spec that imports a `.css.ts` module fails with "Styles were unable to be assigned to a file"
// -- hence a plain `.ts` file with no imports, which both sides can read. Same reason
// `~/styles/palette.ts` exists.
//
// Without it the spec has to restate the declaration, and then a legitimate change to the inset
// fails the spec while the app is correct and consistent -- pointing at the popovers instead of at
// the copy that nobody updated.
export const POPOVER_CARD_PADDING = 'var(--space-2) var(--space-3)'
