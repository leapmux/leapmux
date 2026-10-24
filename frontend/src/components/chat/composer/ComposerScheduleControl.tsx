import type { Component } from 'solid-js'
import CalendarClock from 'lucide-solid/icons/calendar-clock'
import { createEffect, createSignal, Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { actionButtonClass } from '../controls/ControlActionRow'
import * as styles from './ComposerScheduleControl.css'
import { checkSchedule, EMPTY_SCHEDULE, parseLocalSchedule, scheduleIsoOf } from './scheduleSend'

export interface ComposerScheduleControlProps {
  /** The draft `datetime-local` value the panel holds; empty means immediate. */
  value: string
  /**
   * The stored schedule of the item being edited, when one is. An unchanged
   * past deadline is valid to save back; a newly chosen past time is not.
   */
  existingStored?: string
  /** Apply a validated field value, or empty to clear the schedule. */
  onApply: (value: string) => void
  /** The trigger shares the composer's disabled state. */
  disabled?: boolean
}

/**
 * The composer's schedule trigger and its popover.
 *
 * The trigger is a calendar/clock icon with a tooltip -- immediate mode is the
 * default and shows an empty field; a set schedule marks the trigger and
 * changes its accessible name to "Change scheduled send" so a screen reader
 * hears the state, not just the action. The popover is a `div`, not a menu:
 * every click inside it edits the field, and only its own buttons close it.
 */
export const ComposerScheduleControl: Component<ComposerScheduleControlProps> = (props) => {
  // Seeded by the effect below rather than the initializer, so the reactive
  // read of the draft lives in a tracked scope.
  const [field, setField] = createSignal('')
  let popover: HTMLElement | undefined

  // Seed the field from the draft each time the popover opens: the panel can
  // have replaced the draft (an edit started, an edit ended) while it was shut.
  createEffect(() => {
    setField(props.value)
  })

  const scheduled = () => props.value !== EMPTY_SCHEDULE
  const close = () => popover?.hidePopover()

  const verdict = () => checkSchedule(field(), new Date(), props.existingStored)
  const canApply = () => {
    const check = verdict()
    return check === 'empty' || check === 'valid'
  }

  const apply = () => {
    if (!canApply())
      return
    props.onApply(field())
    close()
  }

  const clear = () => {
    props.onApply(EMPTY_SCHEDULE)
    close()
  }

  return (
    <DropdownMenu
      as="div"
      popoverRef={(el) => { popover = el }}
      class={styles.schedulePopover}
      data-testid="schedule-send-popover"
      aria-label="Schedule send"
      trigger={triggerProps => (
        <Tooltip text={scheduled() ? 'Change scheduled send' : 'Schedule send'} ariaLabel>
          <button
            type="button"
            class={actionButtonClass(true)}
            disabled={props.disabled}
            data-testid="schedule-send-trigger"
            data-scheduled={scheduled() ? 'true' : undefined}
            {...triggerProps}
          >
            <Icon icon={CalendarClock} size="sm" />
          </button>
        </Tooltip>
      )}
    >
      <div class={styles.scheduleField}>
        <label for="schedule-send-field">Send not earlier than</label>
        <input
          id="schedule-send-field"
          type="datetime-local"
          step={0.001}
          class={styles.scheduleInput}
          value={field()}
          data-testid="schedule-send-field"
          aria-invalid={verdict() === 'invalid' || verdict() === 'notFuture' ? 'true' : undefined}
          onInput={(e) => { setField(e.currentTarget.value) }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              apply()
            }
          }}
        />
        <div>Uses your local timezone.</div>
        <Show when={!canApply()}>
          <div class={styles.scheduleError} role="alert" data-testid="schedule-send-error">
            {verdict() === 'invalid'
              ? 'Enter a real local date and time.'
              : 'Pick a time in the future.'}
          </div>
        </Show>
      </div>
      <div class={styles.scheduleActions}>
        <button type="button" class="ghost small" data-testid="schedule-send-clear" onClick={clear} disabled={!scheduled()}>
          Clear schedule
        </button>
        <button type="button" class="ghost small" data-testid="schedule-send-cancel" onClick={close}>
          Cancel
        </button>
        <button type="button" class="ghost small" data-testid="schedule-send-apply" onClick={apply} disabled={!canApply()}>
          Schedule
        </button>
      </div>
      {/* The parsed instant the apply would send, stated for the reader and
          asserted by the tests: milliseconds included, UTC on the wire. */}
      <Show when={parseLocalSchedule(field())}>
        {at => <div class={styles.scheduleField} data-testid="schedule-send-iso">{scheduleIsoOf(at())}</div>}
      </Show>
    </DropdownMenu>
  )
}
