import type { LucideIcon } from 'lucide-solid'

import Clock1 from 'lucide-solid/icons/clock-1'
import Clock2 from 'lucide-solid/icons/clock-2'
import Clock3 from 'lucide-solid/icons/clock-3'
import Clock4 from 'lucide-solid/icons/clock-4'
import Clock5 from 'lucide-solid/icons/clock-5'
import Clock6 from 'lucide-solid/icons/clock-6'
import Clock7 from 'lucide-solid/icons/clock-7'
import Clock8 from 'lucide-solid/icons/clock-8'
import Clock9 from 'lucide-solid/icons/clock-9'
import Clock10 from 'lucide-solid/icons/clock-10'
import Clock11 from 'lucide-solid/icons/clock-11'
import Clock12 from 'lucide-solid/icons/clock-12'
import { Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { createSharedTicker } from '~/lib/createSharedTicker'
import { formatCompactAge, formatLocalDateTime, WIDEST_COMPACT_AGE } from '~/lib/dateFormat'
import { steadyTime, steadyTimeContent } from './RelativeTime.css'

const clockIcons: LucideIcon[] = [
  Clock12,
  Clock1,
  Clock2,
  Clock3,
  Clock4,
  Clock5,
  Clock6,
  Clock7,
  Clock8,
  Clock9,
  Clock10,
  Clock11,
]

const REFRESH_INTERVAL_MS = 15_000

// One interval for every mounted RelativeTime, not one each -- the chat view
// renders a timestamp per message, and the file tree keeps a three-dot menu
// mounted per row. See ~/lib/createSharedTicker for the refcount and why an
// instance with an unparseable timestamp subscribes too.
const sharedTick = createSharedTicker(REFRESH_INTERVAL_MS)

interface RelativeTimeProps {
  timestamp: string
  class?: string
  /**
   * Text appended after the relative time, INSIDE the validity guard, so an
   * unparseable timestamp renders nothing at all rather than a bare suffix.
   */
  suffix?: string
  /**
   * Reserve enough width for the clock and the widest age, and align both to the end.
   * The toolbar of a user row sits beside the bubble. If the timestamp grows from
   * `9s` to `10s`, the bubble can wrap to another line and change the row height.
   */
  steadyWidth?: boolean
}

export function RelativeTime(props: RelativeTimeProps) {
  const parsed = () => new Date(props.timestamp)
  const isValid = () => props.timestamp !== '' && !Number.isNaN(parsed().getTime())
  const fullText = () => formatLocalDateTime(parsed())
  const hour12 = () => parsed().getHours() % 12
  const relative = () => {
    sharedTick.tick()
    return isValid()
      ? formatCompactAge(parsed())
      : ''
  }

  sharedTick.subscribe()

  const ClockIcon = () => {
    // `getHours() % 12` is always a valid index into the 12-entry table; `?? Clock12` is the type-level guard alone.
    const ClockFace = clockIcons[hour12()] ?? Clock12
    return <Icon icon={ClockFace} size="xs" />
  }

  return (
    <Show when={isValid()}>
      <Tooltip text={fullText()}>
        <span class={props.class}>
          <Show
            when={props.steadyWidth}
            fallback={(
              <>
                <ClockIcon />
                {' '}
                {relative()}
              </>
            )}
          >
            <span class={steadyTime} data-widest-age={WIDEST_COMPACT_AGE}>
              <span class={steadyTimeContent}>
                <ClockIcon />
                <span>{` ${relative()}`}</span>
              </span>
            </span>
          </Show>
          {props.suffix}
        </span>
      </Tooltip>
    </Show>
  )
}

/**
 * {@link RelativeTime} followed by " ago" -- the form every context-menu info
 * block uses for a timestamp.
 *
 * The suffix goes through `RelativeTime`'s own `suffix` prop rather than
 * sitting beside the element, so it shares the one `isValid()` guard. Rendered
 * as a sibling it survived that guard, and a menu whose stat carried a
 * non-empty but unparseable timestamp showed a row reading "Modified:" with a
 * bare "ago" and no time.
 */
export function RelativeTimeAgo(props: { timestamp: string }) {
  return <RelativeTime timestamp={props.timestamp} suffix=" ago" />
}
