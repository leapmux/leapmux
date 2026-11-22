import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { rows as backgroundTaskRows } from '~/components/backgroundtasks/BackgroundTaskPanel.css'
import { previewCard } from '~/components/chat/ChatScrollRail.css'
import { comboboxListbox } from '~/components/common/FilterableListbox.css'
import { filteredList } from '~/components/common/LoadingMenu.css'
import { tooltip } from '~/components/common/Tooltip.css'
import { floatingCardSurface, popoverBase, popoverCard, popoverCardPadding, popoverColumnClamp, touchScrollContain } from '~/styles/popover.css'
import { POPOVER_CARD_PADDING } from '~/styles/popoverTokens'
import { extractStyleCallBlock } from '~/test-support/styleSource'

const popoverCssSource = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), 'popover.css.ts'),
  'utf8',
)

describe('popoverCard', () => {
  it('carries Oat\'s own card class, which is where the surface comes from', () => {
    // Background, border, radius and shadow are NOT declared here: Oat's `card`
    // rule sets them, so every card popover follows Oat and two surfaces of the
    // same card cannot look different.
    //
    // Nothing else catches a dropped `card`. jsdom loads no Oat stylesheet, so
    // no component test can see the surface at all, and the surviving classes
    // still position the popover correctly -- the cards would simply lose their
    // background on a real page, which only the e2e run measures.
    expect(popoverCard.split(' ')).toContain('card')
  })

  it('carries the compact inset, which Oat\'s card padding is NOT the source of', () => {
    // Oat pads a card by var(--space-6), which is right for a card that fills the
    // page (the auth forms) and too much for one that opens over the reader's
    // work. This class is what overrides it, and it is shared with the chat
    // rail's preview card so the two cannot drift.
    expect(popoverCard.split(' ')).toContain(popoverCardPadding)
  })

  it('carries the positioning reset too, so a call site needs no second class', () => {
    // vanilla-extract emits every composed class, so the list is: Oat's `card`,
    // popoverBase, touchScrollContain, the layout class that composes them, and
    // the padding class. popoverBase is what resets the UA popover defaults and
    // stops a CLOSED popover from swallowing the next click.
    const classes = popoverCard.split(' ')
    expect(classes).toContain(popoverBase)
    expect(classes).toContain(touchScrollContain)
    expect(classes).toHaveLength(5)
  })
})

describe('floatingCardSurface', () => {
  it('takes Oat\'s card fill, border and radius rather than restating them', () => {
    // The whole point of the class: a floating card FOLLOWS Oat's card, so it cannot end up
    // painted a different colour from every other card in the app. That is not hypothetical --
    // the rail's preview card filled var(--background) while the tooltip filled var(--card),
    // which is the drift this class exists to make impossible.
    expect(floatingCardSurface.split(' ')).toContain('card')
  })

  it('takes the compact inset, not Oat\'s page-card padding', () => {
    expect(floatingCardSurface.split(' ')).toContain(popoverCardPadding)
  })

  it('is the surface BOTH floating surfaces in the app carry', () => {
    // A tooltip and the chat rail's preview card are the same kind of thing -- a small surface
    // that opens over the reader's work -- and each used to write the fill, border, radius,
    // shadow, inset and line height out for itself. jsdom loads no Oat stylesheet, so the class
    // list is the only thing a unit test can see; the resolved pixels are e2e territory.
    //
    // Compared token by token: vanilla-extract FLATTENS a composed class into the consumer's own
    // list, so the composite arrives as several class names and never as one string to match.
    const surface = floatingCardSurface.split(' ')
    expect(tooltip.split(' ')).toEqual(expect.arrayContaining(surface))
    expect(previewCard.split(' ')).toEqual(expect.arrayContaining(surface))
  })
})

describe('popoverCardPadding', () => {
  it('is built from the plain token the e2e run also reads', () => {
    // The value lives in a plain `.ts` file because Playwright cannot import a `.css.ts` module,
    // and the e2e spec measures this inset. If the spec restated the declaration instead, a
    // legitimate change to the inset would fail the spec while the app stayed correct.
    expect(POPOVER_CARD_PADDING).toBe('var(--space-2) var(--space-3)')
  })
})

describe('popover viewport clamp', () => {
  it('uses bodyHeight rather than bare vh units so popovers shrink above the iOS keyboard', () => {
    expect(popoverCssSource).toMatch(/VIEWPORT_MAX_HEIGHT\s*=\s*`calc\(\$\{bodyHeight\}\s*-\s*var\(--space-6\)\s*\*\s*2\)`/)
    expect(popoverCssSource).toMatch(/dialogHeight\s*=\s*`var\(\$\{DIALOG_HEIGHT_VAR\},\s*\$\{bodyHeight\}\)`/)
    expect(extractStyleCallBlock(popoverCssSource, 'popoverPanelClamp')).toMatch(/maxHeight:\s*`calc\(\$\{bodyHeight\}\s*\*\s*0\.6\)`/)
    expect(popoverCssSource).not.toMatch(/\b\d+vh\b/)
  })

  it('shares touchScrollContain across popoverColumnClamp and nested popover/panel scrollers', () => {
    const block = extractStyleCallBlock(popoverCssSource, 'touchScrollContain')
    expect(block).toMatch(/overscrollBehavior:\s*'contain'/)
    expect(block).toMatch(/WebkitOverflowScrolling:\s*'touch'/)

    expect(popoverColumnClamp.split(' ')).toContain(touchScrollContain)
    expect(comboboxListbox.split(' ')).toContain(touchScrollContain)
    expect(filteredList.split(' ')).toContain(touchScrollContain)
    expect(backgroundTaskRows.split(' ')).toContain(touchScrollContain)
  })
})
