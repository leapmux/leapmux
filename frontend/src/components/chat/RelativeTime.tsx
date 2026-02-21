import type { Component } from 'solid-js'

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
import { createSignal, onCleanup, onMount, Show } from 'solid-js'
import { iconSize } from '~/styles/tokens'

const clockIcons: Component<{ size: number }>[] = [
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

/** Format a duration as a compact string (e.g. "3s", "12m", "2h", "5d", "3mo", "2y"). */
function formatCompact(ts: Date): string {
  const diffSec = Math.max(0, Math.floor((Date.now() - ts.getTime()) / 1000))
  if (diffSec < 60)
    return `${diffSec}s`
  const diffMin = Math.floor(diffSec / 60)
  if (diffMin < 60)
    return `${diffMin}m`
  const diffHr = Math.floor(diffMin / 60)
  if (diffHr < 24)
    return `${diffHr}h`
  const diffDay = Math.floor(diffHr / 24)
  if (diffDay < 30)
    return `${diffDay}d`
  const diffMo = Math.floor(diffDay / 30)
  if (diffMo < 12)
    return `${diffMo}mo`
  const diffYr = Math.floor(diffDay / 365)
  return `${diffYr}y`
}

/** Format a Date as "YYYY-MM-DD HH:mm:ss". */
function formatFull(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

interface RelativeTimeProps {
  timestamp: string
  class?: string
}

export function RelativeTime(props: RelativeTimeProps) {
  const parsed = () => new Date(props.timestamp)
  const isValid = () => props.timestamp !== '' && !Number.isNaN(parsed().getTime())
  const fullText = () => formatFull(parsed())
  const hour12 = () => parsed().getHours() % 12
  const [relative, setRelative] = createSignal(isValid() ? formatCompact(parsed()) : '')

  let timer: ReturnType<typeof setInterval> | undefined

  onMount(() => {
    if (!isValid())
      return
    timer = setInterval(() => {
      setRelative(formatCompact(parsed()))
    }, 15_000)
  })

  onCleanup(() => {
    if (timer !== undefined)
      clearInterval(timer)
  })

  const ClockIcon = () => {
    const Icon = clockIcons[hour12()]
    return <Icon size={iconSize.xs} />
  }

  return (
    <Show when={isValid()}>
      <span class={props.class} title={fullText()}>
        <ClockIcon />
        {' '}
        {relative()}
      </span>
    </Show>
  )
}
