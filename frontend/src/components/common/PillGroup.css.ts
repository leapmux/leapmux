import { style } from '@vanilla-extract/css'

/** One content-sized control with one outer border. */
export const pillGroup = style({
  position: 'relative',
  isolation: 'isolate',
  display: 'inline-flex',
  width: 'max-content',
  maxWidth: '100%',
  gap: 0,
  overflow: 'hidden',
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
})

/** Dim a group that refuses all changes. */
export const pillGroupDisabled = style({
  opacity: 0.55,
})

/** Metrics that the real buttons and their visual copies share. */
const pillOptionLayout = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  minWidth: 0,
  flexShrink: 1,
  padding: 'var(--space-2) var(--space-4)',
  border: 0,
  borderRadius: 0,
  fontSize: 'var(--text-7)',
  lineHeight: 'var(--leading-normal)',
  fontWeight: 'var(--font-normal)',
  whiteSpace: 'normal',
  overflowWrap: 'anywhere',
})

/** Shape and behavior for each real radio. */
export const pillOption = style([pillOptionLayout, {
  position: 'relative',
  zIndex: 1,
  backgroundColor: 'transparent',
  color: 'var(--muted-foreground)',
  opacity: 1,
  cursor: 'pointer',
  transitionProperty: 'none',
  selectors: {
    '&:hover:not(:disabled):not([aria-disabled="true"]):not([aria-checked="true"])': {
      backgroundColor: 'var(--accent)',
      color: 'var(--foreground)',
    },
    '&:active:not(:disabled):not([aria-disabled="true"]):not([aria-checked="true"])': {
      transform: 'none',
    },
    '&:focus-visible': {
      outline: '2px solid var(--ring)',
      outlineOffset: '-2px',
    },
    '&:disabled': {
      cursor: 'default',
    },
  },
}])

/** Keep each boundary visible between adjacent segments. */
export const pillOptionSeparated = style({
  borderInlineStart: '1px solid var(--border)',
})

/** Dim an unavailable option that is not selected. */
export const pillOptionDimmed = style({
  opacity: 0.55,
})

/** Mark an option that can receive focus but refuses selection. */
export const pillOptionUnavailable = style({
  cursor: 'not-allowed',
})

/** Paint the complete selected state when the sliding layers are unavailable. */
export const pillOptionActive = style({
  'backgroundColor': 'var(--primary)',
  'color': 'var(--primary-foreground)',
  'selectors': {
    '&:hover:not(:disabled):not([aria-disabled="true"])': {
      backgroundColor: 'var(--primary)',
      color: 'var(--primary-foreground)',
    },
    '&:focus-visible': {
      outlineColor: 'var(--primary-foreground)',
    },
  },
  '@media': {
    '(forced-colors: active)': {
      forcedColorAdjust: 'none',
      backgroundColor: 'Highlight',
      color: 'HighlightText',
      selectors: {
        '&:hover:not(:disabled):not([aria-disabled="true"])': {
          backgroundColor: 'Highlight',
          color: 'HighlightText',
        },
        '&:focus-visible': {
          outlineColor: 'HighlightText',
        },
      },
    },
  },
})

/** Mark the selected target after the fill reaches it. */
export const pillOptionSelectedTarget = style({
  'boxShadow': 'inset 0 0 0 2px var(--primary)',
  'selectors': {
    '&:focus-visible': {
      boxShadow: 'inset 0 0 0 4px var(--primary)',
      outlineColor: 'var(--primary-foreground)',
    },
  },
  '@media': {
    '(forced-colors: active)': {
      boxShadow: 'inset 0 0 0 2px Highlight',
      selectors: {
        '&:focus-visible': {
          boxShadow: 'inset 0 0 0 4px Highlight',
          outlineColor: 'HighlightText',
        },
      },
    },
  },
})

const selectionWindow = style({
  position: 'absolute',
  inset: 0,
  clipPath: 'inset(0 var(--pill-selection-right) 0 var(--pill-selection-left))',
  pointerEvents: 'none',
  transitionProperty: 'none',
})

/** The moving primary background. The real selected button remains outlined. */
export const selectionFill = style([selectionWindow, {
  'zIndex': 0,
  'backgroundColor': 'var(--primary)',
  '@media': {
    '(forced-colors: active)': {
      forcedColorAdjust: 'none',
      backgroundColor: 'Highlight',
    },
  },
}])

/** Active-color label copies that move with the primary background. */
export const selectionLabels = style([selectionWindow, {
  'zIndex': 2,
  'display': 'flex',
  'color': 'var(--primary-foreground)',
  '@media': {
    '(forced-colors: active)': {
      forcedColorAdjust: 'none',
      color: 'HighlightText',
    },
  },
}])

/** One label copy. It has the exact metrics of its real radio. */
export const selectionLabel = style([pillOptionLayout, {
  backgroundColor: 'transparent',
  color: 'inherit',
  selectors: {
    '&::before': {
      content: 'attr(data-label)',
    },
  },
}])

/** Slide both clipped layers after the first valid measurement. */
export const selectionWindowMoves = style({
  'transition': 'clip-path var(--transition)',
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transitionProperty: 'none',
    },
  },
})
