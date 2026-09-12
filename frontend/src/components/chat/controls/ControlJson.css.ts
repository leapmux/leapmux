import { style } from '@vanilla-extract/css'
import { bannerCodeBlock } from '../ControlRequestBanner.css'

export const root = style({ position: 'relative', minWidth: 0, maxWidth: '100%' })

export const probe = style([bannerCodeBlock, {
  position: 'absolute',
  display: 'inline-block',
  width: '10ch',
  height: '1px',
  visibility: 'hidden',
  pointerEvents: 'none',
  whiteSpace: 'pre',
}])

export const json = style([bannerCodeBlock, {
  whiteSpace: 'pre',
  wordBreak: 'normal',
  overflowX: 'auto',
  maxWidth: '100%',
}])
