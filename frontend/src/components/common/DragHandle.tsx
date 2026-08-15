import type { JSX } from 'solid-js'
import type { DragActivatorProps } from '~/lib/dragActivators'
import GripVertical from 'lucide-solid/icons/grip-vertical'
import { attachDragActivators } from '~/lib/dragActivators'
import * as styles from '~/styles/dragHandle.css'

interface DragHandleProps {
  /**
   * Accessor for the RAW `dragActivators` of the row's draggable/sortable —
   * unguarded, both inputs. An accessor (not a value) because the handlers a
   * `dragActivators` getter returns only list sensors registered at call
   * time; `attachDragActivators` re-reads it reactively.
   */
  activators: () => DragActivatorProps | undefined
  /**
   * `auto` (default) hides the grip wherever the system has a fine pointer,
   * so it appears only on touch-only devices; `always` shows it, for
   * touch-first surfaces such as the mobile tab sheet.
   */
  visibility?: 'auto' | 'always'
  class?: string
  testId?: string
}

/**
 * The touch reorder affordance. A dedicated grip is the only place a touch
 * drag may start (row bodies carry mouse-only activators instead), which is
 * what lets the stock upstream pointer sensor serve touch without a fork:
 * swipe-a-row scrolls, hold-a-row opens the context menu, and press-grip-
 * and-move drags — immediately, with no hold window to race.
 *
 * aria-hidden: the grip is a pointer-only affordance. It has no activation
 * semantics of its own — it can't be clicked or focused, it only hands a
 * press to the drag machinery — so there is nothing to announce.
 */
export function DragHandle(props: DragHandleProps): JSX.Element {
  let ref: HTMLElement | undefined
  // The call itself owns the tracked scope (a createEffect inside), so the
  // untracked-looking accessor below is the whole point.
  attachDragActivators(
    () => ref,
    // eslint-disable-next-line solid/reactivity
    () => props.activators(),
    { touch: 'allow' },
  )
  return (
    <span
      ref={(el) => {
        ref = el
      }}
      class={`${props.visibility === 'always' ? styles.dragHandle : styles.dragHandleAuto} ${props.class ?? ''}`}
      data-drag-handle=""
      data-testid={props.testId}
      aria-hidden="true"
    >
      <GripVertical size={14} />
    </span>
  )
}
