import { globalStyle, style } from '@vanilla-extract/css'
import { POPOVER_CARD_PADDING } from '~/styles/popoverTokens'

/**
 * A dropdown host with no trigger produces no box.
 *
 * `ot-dropdown` is an unknown element to the UA, so it defaults to
 * `display: inline`. A trigger-less `DropdownMenu` -- one opened only by
 * right-click or long press -- holds nothing but its `position: fixed` popover, so
 * that inline box is empty. Inside a flex row it is still a flex ITEM, and it adds
 * one `gap` of dead space to every row that mounts such a menu.
 *
 * `DropdownMenu` sets the attribute; see `data-headless` there.
 */
globalStyle('ot-dropdown[data-headless]', {
  display: 'contents',
})

/**
 * The base class a `popover="auto"` element needs when it is positioned by JS (an
 * explicit top/left written by our own positioning code) rather than by the UA default.
 * Compose it into each popover style via `style([popoverBase, { ...own }])` -- as a
 * COMPOSED class (not a spread rule) so both classes land on the element and a consumer's
 * own `&:popover-open` block (a grid display, an opacity/transform reveal) ADDS to the
 * base's `display: flex` instead of shallow-overriding it.
 *
 * Two rules, both load-bearing:
 *
 *  - `position: fixed; margin: 0` resets the UA popover defaults (`inset: 0; margin: auto`).
 *    Without the `margin: 0`, `margin: auto` re-centers the popover in the viewport even
 *    after our code sets top/left -- which clipped it and left a large dead area.
 *  - `display: flex` applies only under `:popover-open`. An author `display` set unconditionally
 *    beats the UA `[popover]:not(:popover-open) { display: none }` rule (author origin wins
 *    over UA regardless of specificity), so a bare `display: flex` would keep the popover
 *    laid out + visible (and, being `position: fixed`, covering the page) after it closes.
 *  - `pointer-events: none` while CLOSED. Oat's own `ot-dropdown [popover]` rule animates
 *    the close with `display` and `overlay` in `allow-discrete`, so for the length of that
 *    transition a closed popover is still laid out, still in the top layer, and still
 *    hit-testable — it swallows the very next click, wherever the user aimed it. A popover
 *    anchored over its own trigger therefore could not be reopened: the click that should
 *    have reopened it landed on the fading corpse instead. Gating hit-testing on the open
 *    state fixes that without giving up the reveal animation.
 *
 * Single-sourced here so a new popover can't re-discover the "stays visible after close" /
 * "margin:auto re-centers" / "eats the next click while closing" bugs the hard way.
 */
/**
 * How close a popover may come to the edge of the screen. Written once, because
 * `popoverColumnClamp` and `popoverMenuClamp` both state it and a menu's own
 * clamp is the viewport term of a `min()`.
 */
const VIEWPORT_MAX_WIDTH = 'calc(100vw - var(--space-4) * 2)'
const VIEWPORT_MAX_HEIGHT = 'calc(100vh - var(--space-6) * 2)'

export const popoverBase = style({
  position: 'fixed',
  margin: 0,
  selectors: {
    '&:popover-open': {
      display: 'flex',
    },
    '&:not(:popover-open)': {
      pointerEvents: 'none',
    },
  },
})

/**
 * `popoverBase` plus a column layout clamped to the viewport.
 *
 * A popover sizes to its content, and content that grows without limit (a long
 * rate-limit list, a long list of to-dos, a long option catalog) has nothing
 * else to stop it running off the screen. Clamp both axes and scroll the
 * overflow instead.
 *
 * Deliberately NOT folded into `popoverBase`, although every consumer of this
 * class composes that one. `popoverBase` also carries the link and code-language
 * popovers in `~/components/chat/markdownEditor/MarkdownEditor.css.ts`, and that
 * file records why `overflow-y` must not reach them: with `overflow-y` inherited
 * from the popover chrome, CSS computes `overflow-x` to `auto` as well, and the
 * link card grew a horizontal scrollbar that pushed its remove button out of
 * view. One of them also sets a competing `max-width` of its own.
 */
export const popoverColumnClamp = style([popoverBase, {
  flexDirection: 'column',
  maxWidth: VIEWPORT_MAX_WIDTH,
  maxHeight: VIEWPORT_MAX_HEIGHT,
  overflowY: 'auto',
}])

/**
 * The custom properties `DropdownMenu` measures onto its popover, so a MENU can
 * follow the control it opens from instead of the whole viewport.
 *
 * The names are spelled here and read through these constants at the one place
 * that writes them, so the stylesheet and the component cannot drift.
 */
export const TRIGGER_WIDTH_VAR = '--dropdown-trigger-width'
export const DIALOG_HEIGHT_VAR = '--dropdown-dialog-height'

/**
 * The clamp EVERY menu popover takes: `popoverColumnClamp`, with the dialog that
 * holds the trigger as a tighter height limit than the viewport.
 *
 * `calcPopoverPosition` clamps where a popover STARTS, not how large it grows,
 * so a menu of fifty rows ran off the bottom of the screen and the rows past the
 * bottom edge could not be reached at all. A dialog is also a box the reader
 * takes in as one thing, and a menu taller than it reads as a second window over
 * the page rather than as the open form of a field inside it. The viewport stays
 * as the outer term, for a menu no dialog holds.
 *
 * `overflow` on BOTH axes, where `popoverColumnClamp` scrolls only vertically:
 * the horizontal half is the swipe that reaches a row too wide to fit, on a
 * narrow field where the checked-state radio and a detail take room the label
 * cannot give up.
 *
 * Deliberately NO width rule. See `popoverFieldMenuClamp`.
 */
const triggerWidth = `var(${TRIGGER_WIDTH_VAR}, 100vw)`
const dialogHeight = `var(${DIALOG_HEIGHT_VAR}, 100vh)`

export const popoverMenuClamp = style([popoverColumnClamp, {
  maxHeight: `min(${dialogHeight}, ${VIEWPORT_MAX_HEIGHT})`,
  overflow: 'auto',
  // A swipe that reaches the end of the list must not carry on into whatever
  // sits behind the menu.
  overscrollBehavior: 'contain',
}])

/**
 * The extra clamp a FIELD-shaped menu takes: never wider than its own trigger.
 *
 * A field's menu is the open form of the control the user clicked, so the two
 * read as one control when their edges line up, and a row longer than the field
 * clips instead of pushing the box out over the page.
 *
 * OPT-IN, and that is the whole point of the split. A menu's trigger is not
 * always a field: a kebab is a 24px icon button, and capping its menu at its
 * trigger leaves a 24px column with every row unreadable. Only a caller that
 * knows its trigger is field-shaped asks for this -- `DropdownMenu`'s
 * `matchTriggerWidth`, which `LoadingMenu` sets and nothing else does.
 *
 * `min-width` is restated because Oat's own `ot-dropdown [popover]` rule sets
 * `min-width: 12rem`, and a min-width ALWAYS beats a max-width -- a trigger
 * narrower than 12rem would otherwise keep a popover wider than itself.
 */
export const popoverFieldMenuClamp = style([popoverMenuClamp, {
  maxWidth: `min(${triggerWidth}, ${VIEWPORT_MAX_WIDTH})`,
  minWidth: `min(12rem, ${triggerWidth})`,
}])

/**
 * The inset a card gives its content when it FLOATS over the reader's work.
 *
 * Oat's own `card` rule pads by `var(--space-6)` on each side. That suits a card that IS the
 * page -- the login, signup, and setup forms, which fill a centred column and are the only
 * thing on screen. It is too much for a card that opens OVER that work. The reader takes a
 * popover in at a glance and dismisses it, and a 24px edge on each side pushes its rows apart
 * and its content toward the edge of the screen. So a floating card takes the compact inset
 * instead: the one the chat rail's message-preview card already used.
 *
 * Declared as its own class, not folded into `popoverColumnClamp`, because that class also
 * carries the composer's MENU popovers, whose items pad themselves.
 *
 * It beats Oat's padding by LAYER, not by specificity -- the two selectors are both (0,1,0), so
 * specificity ties. Oat declares `@layer theme,base,components,animations,utilities` and puts
 * `.card` in `components`; this class is unlayered, and unlayered author CSS outranks every
 * author layer. `~/styles/global.css.ts` records the same mechanic for the menu-item rules.
 *
 * The value itself lives in `~/styles/popoverTokens.ts`, a plain `.ts` file, because the e2e run
 * measures this inset and Playwright cannot import a `.css.ts` module. One declaration, two
 * readers, no restatement to drift.
 */
export const popoverCardPadding = style({
  padding: POPOVER_CARD_PADDING,
})

/**
 * The whole surface of a card that FLOATS over the reader's work: Oat's card fill, border and
 * radius, the compact inset above, and the lift that separates it from the page.
 *
 * One class, because the app kept re-deriving this surface by hand and the copies drifted. The
 * chat rail's preview card and `~/components/common/Tooltip.css.ts` each wrote out the same
 * border, radius, shadow, colour, line height and inset -- and disagreed on the one that matters
 * most: the tooltip filled `var(--card)` while the rail's card filled `var(--background)`, so the
 * only floating surface in the app painted the page colour was separated from the transcript
 * behind it by a 1px border alone. That is hardest to see in dark theme, where the two tokens are
 * a shade apart.
 *
 * The shadow is NOT Oat's `--shadow-small`. That token is the subtle lift of a card sitting IN the
 * page; a card floating OVER it needs a shadow the reader reads as depth, which is the value both
 * hand-rolled copies had already converged on.
 *
 * Composes Oat's `card` as a plain class name (vanilla-extract accepts one in a style list), so
 * the fill, the border and the radius follow Oat rather than being restated here.
 */
export const floatingCardSurface = style(['card', popoverCardPadding, {
  boxShadow: '0 2px 8px rgba(0, 0, 0, 0.15)',
  lineHeight: 1.4,
}])

/**
 * The class list for a popover whose content is a CARD -- labelled rows, a list,
 * a panel -- and not a list of menu items. Apply it whole:
 * `<DropdownMenu as="div" class={popoverCard}>`.
 *
 * It is a class LIST, and every part is load-bearing. Oat's own `card` rule supplies the
 * surface (background, border, radius, shadow). `popoverCardPadding` supplies the inset, so
 * every card popover insets its content the same way -- two surfaces of the SAME card cannot
 * drift apart, which is what the agent-info card did while each call site set its own padding.
 * `popoverColumnClamp` adds what a popover needs on top of a card: the positioning reset and
 * the viewport clamp.
 *
 * Exported as one string so that no call site can apply half of it.
 */
export const popoverCard = `card ${popoverColumnClamp} ${popoverCardPadding}`
