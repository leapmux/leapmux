import { Tooltip } from '~/components/common/Tooltip'
import { steerNoteIcon, steerNoteMarker } from './SteerNoteMarker.css'

/** One inline note that a model echoed about a steer the reader sent mid-turn. */
export function SteerNoteMarker(props: { steerId: string, note: string }) {
  return (
    <Tooltip text={`Steer ${props.steerId}`} ariaLabel>
      <span class={steerNoteMarker} data-testid="steer-note-marker">
        <span class={steerNoteIcon} aria-hidden="true">»</span>
        {props.note}
      </span>
    </Tooltip>
  )
}
