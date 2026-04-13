import type { JSX } from 'solid-js'
import Maximize2 from 'lucide-solid/icons/maximize-2'
import ZoomIn from 'lucide-solid/icons/zoom-in'
import ZoomOut from 'lucide-solid/icons/zoom-out'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import * as styles from './FileViewer.css'

export type ZoomMode = 'fit' | 'actual' | number

export const ZOOM_STEP = 0.25
export const ZOOM_MIN = 0.25
export const ZOOM_MAX = 5

export function zoomLabel(mode: ZoomMode, fitScale?: number | null): string {
  if (mode === 'fit') {
    if (fitScale != null)
      return `${Number.parseFloat((fitScale * 100).toFixed(1))}%`
    return 'Fit'
  }
  if (mode === 'actual')
    return '100%'
  return `${Math.round(mode * 100)}%`
}

export function zoomIn(current: ZoomMode, fitScale?: number | null): ZoomMode {
  const scale = current === 'fit'
    ? (fitScale ?? 1)
    : current === 'actual' ? 1 : current
  const next = Math.round((scale + ZOOM_STEP) * 100) / 100
  return Math.min(next, ZOOM_MAX)
}

export function zoomOut(current: ZoomMode, fitScale?: number | null): ZoomMode {
  const scale = current === 'fit'
    ? (fitScale ?? 1)
    : current === 'actual' ? 1 : current
  const next = Math.round((scale - ZOOM_STEP) * 100) / 100
  return Math.max(next, ZOOM_MIN)
}

export function ImageToolbar(props: {
  zoom: ZoomMode
  fitScale?: number | null
  onZoomChange: (mode: ZoomMode) => void
}): JSX.Element {
  return (
    <div class={styles.imageToolbar}>
      <Tooltip text="Zoom out" targetAriaLabel>
        <button
          class={styles.imageToolbarButton}
          onClick={() => props.onZoomChange(zoomOut(props.zoom, props.fitScale))}
        >
          <Icon icon={ZoomOut} size="sm" />
        </button>
      </Tooltip>
      <span class={styles.imageToolbarLabel}>{zoomLabel(props.zoom, props.fitScale)}</span>
      <Tooltip text="Zoom in" targetAriaLabel>
        <button
          class={styles.imageToolbarButton}
          onClick={() => props.onZoomChange(zoomIn(props.zoom, props.fitScale))}
        >
          <Icon icon={ZoomIn} size="sm" />
        </button>
      </Tooltip>
      <Tooltip text="Fit to view" targetAriaLabel>
        <button
          class={styles.imageToolbarButton}
          onClick={() => props.onZoomChange('fit')}
        >
          <Icon icon={Maximize2} size="sm" />
        </button>
      </Tooltip>
      <Tooltip text="Actual size (100%)" targetAriaLabel>
        <button
          class={styles.imageToolbarTextButton}
          onClick={() => props.onZoomChange('actual')}
        >
          100%
        </button>
      </Tooltip>
    </div>
  )
}
