import { style } from '@vanilla-extract/css'
import { fieldTrigger } from '~/styles/shared.css'

export const trigger = style([fieldTrigger, {
  gap: 'var(--space-1)',
  // Never shrinks: the input beside it takes the slack instead, so a long path
  // cannot push the drive off the row.
  flexShrink: 0,
  padding: 'var(--space-1) var(--space-2)',
}])

export const triggerValue = style({
  whiteSpace: 'nowrap',
})

// Only what differs from Oat's own `ot-dropdown [popover]` rule, which already
// supplies the background, the border, the radius and `margin: 0`. Restating
// them here would be four declarations that say what the layer below already
// says, and an unlayered class outranks it -- so a change to the design system
// would stop reaching this menu.
export const menu = style({
  minWidth: '10rem',
  padding: 'var(--space-1)',
  // Oat's default is `--shadow-small`. The sibling dropdown this menu sits
  // beside (`AgentProviderSelector`) lifts it, and two menus in one dialog
  // must not float at two different heights.
  boxShadow: 'var(--shadow-medium)',
})
