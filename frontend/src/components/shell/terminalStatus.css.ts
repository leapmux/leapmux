import { style } from '@vanilla-extract/css'

// Styles shared by terminal tab labels in both the TabBar and the sidebar
// tab tree. Disconnected terminals fade slightly; exited terminals also gain
// a strikethrough.

export const disconnected = style({
  opacity: 0.8,
})

export const exited = style({
  opacity: 0.8,
  textDecoration: 'line-through',
})
