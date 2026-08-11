import { describe, expect, it } from 'vitest'
import { popoverBase, popoverCard } from '~/styles/popover.css'

describe('popoverCard', () => {
  it('carries Oat\'s own card class, which is where the inset comes from', () => {
    // The padding is NOT declared here: Oat's `card` rule sets it
    // (`var(--space-6)` on each side), so every card popover follows Oat and
    // two surfaces of the same card cannot inset their content differently.
    //
    // Nothing else catches a dropped `card`. jsdom loads no Oat stylesheet, so
    // no component test can see padding at all, and the surviving class still
    // positions the popover correctly -- the cards would simply lose their inset
    // on a real page, which only the e2e run measures.
    expect(popoverCard.split(' ')).toContain('card')
  })

  it('carries the positioning reset too, so a call site needs no second class', () => {
    // vanilla-extract emits every composed class, so the list is: Oat's `card`,
    // popoverBase, and the layout class that composes it. popoverBase is what
    // resets the UA popover defaults and stops a CLOSED popover from swallowing
    // the next click, and a bare `card` popover had neither -- which is the
    // second half of what applying this class whole buys.
    const classes = popoverCard.split(' ')
    expect(classes).toContain(popoverBase)
    expect(classes).toHaveLength(3)
  })
})
