import type { Component } from 'solid-js'
import CalendarClock from 'lucide-solid/icons/calendar-clock'
import { createSignal, createUniqueId, onCleanup, Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { actionButtonClass } from '../controls/ControlActionRow'
import * as styles from './ComposerScheduleControl.css'
import { checkSchedule, EMPTY_SCHEDULE, scheduleRefusalMessage } from './scheduleSend'

/**
 * How often an open popover reads the clock again. A chosen time that passes
 * while the user looks at the popover then refuses itself on screen, instead
 * of only when the user presses Schedule.
 */
export const SCHEDULE_CLOCK_TICK_MS = 1000

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
 * hears the state, not just the action. The popover is a card, not a menu:
 * every click inside it edits the field, the arrow keys step the field
 * instead of moving between the buttons, and only its own buttons close it.
 */
export const ComposerScheduleControl: Component<ComposerScheduleControlProps> = (props) => {
  const fieldId = createUniqueId()
  const [field, setField] = createSignal(EMPTY_SCHEDULE)
  // The instant that the verdict compares the field against. It ticks only
  // while the popover is open.
  const [now, setNow] = createSignal(new Date())
  let clockTimer: ReturnType<typeof setInterval> | undefined
  let popover: HTMLElement | undefined

  const stopClock = () => {
    if (clockTimer !== undefined) {
      clearInterval(clockTimer)
      clockTimer = undefined
    }
  }
  onCleanup(stopClock)

  // Each opening starts from the panel's draft. A value typed before a Cancel
  // or a light dismiss is not applied, so it must not come back, and the panel
  // can replace the draft while the popover is shut (an edit starts or ends).
  const handleToggle = (open: boolean) => {
    stopClock()
    if (!open)
      return
    setField(props.value)
    setNow(new Date())
    clockTimer = setInterval(() => setNow(new Date()), SCHEDULE_CLOCK_TICK_MS)
  }

  const scheduled = () => props.value !== EMPTY_SCHEDULE
  const close = () => popover?.hidePopover()

  /** Why the field cannot be applied, or undefined when it can. */
  const refusal = () => {
    const { verdict } = checkSchedule(field(), now(), props.existingStored)
    return verdict === 'invalid' || verdict === 'notFuture' ? scheduleRefusalMessage(verdict) : undefined
  }

  const apply = () => {
    // The verdict that enabled the button can be one tick old, so read the
    // clock again. A refusal then shows its message instead of doing nothing.
    setNow(new Date())
    if (refusal() !== undefined)
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
      as="card"
      popoverRef={(el) => { popover = el }}
      onToggle={handleToggle}
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
      <div class={styles.scheduleBody}>
        <div class={styles.scheduleField}>
          <label for={fieldId}>Send not earlier than</label>
          <input
            id={fieldId}
            type="datetime-local"
            step={0.001}
            class={styles.scheduleInput}
            value={field()}
            data-testid="schedule-send-field"
            aria-invalid={refusal() !== undefined ? 'true' : undefined}
            onInput={(e) => { setField(e.currentTarget.value) }}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                apply()
              }
            }}
          />
          <div>Uses your local timezone.</div>
          <Show when={refusal()}>
            {message => (
              <div class={styles.scheduleError} role="alert" data-testid="schedule-send-error">
                {message()}
              </div>
            )}
          </Show>
        </div>
        <div class={styles.scheduleActions}>
          <button type="button" class="ghost small" data-testid="schedule-send-clear" onClick={clear} disabled={!scheduled()}>
            Clear schedule
          </button>
          <button type="button" class="ghost small" data-testid="schedule-send-cancel" onClick={close}>
            Cancel
          </button>
          <button type="button" class="ghost small" data-testid="schedule-send-apply" onClick={apply} disabled={refusal() !== undefined}>
            Schedule
          </button>
        </div>
      </div>
    </DropdownMenu>
  )
}
