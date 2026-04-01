import { globalStyle, style } from '@vanilla-extract/css'

export const compactSwitch = style({
  marginBottom: 0,
})

globalStyle(`${compactSwitch} input[type="checkbox"][role="switch"]`, {
  height: '1.25em',
  width: '2.25em',
})

globalStyle(`${compactSwitch} input[type="checkbox"][role="switch"]::before`, {
  height: 'calc(1.25em - 4px)',
  width: 'calc(1.25em - 4px)',
  top: '50%',
  transform: 'translate(0, -50%)',
})

globalStyle(`${compactSwitch} input[type="checkbox"][role="switch"]:checked::before`, {
  transform: 'translate(calc(2.25em - 100% - 4px), -50%)',
})
