import type { Component } from 'solid-js'
import { createMemo, Show } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { formatBytes } from '~/lib/formatBytes'
import { AnimatedCount } from './AnimatedCount'

const MINIMUM_OUTPUT_TOOLTIP = 'The provider limits live output. This count is a minimum.'

export const ThinkingOutputCount: Component<{ bytes: number, minimum?: boolean, paused?: boolean }> = (props) => {
  const formatted = createMemo(() => formatBytes(props.bytes))
  const split = createMemo(() => {
    const [display, unit = 'B'] = formatted().split(' ')
    return { display: `${props.minimum ? '≥' : ''}${display}`, unit }
  })
  const content = () => <AnimatedCount display={split().display} unit={split().unit} family={split().unit} paused={props.paused} />
  return (
    <span data-testid="thinking-output-count">
      <Show when={props.minimum} fallback={content()}>
        <Tooltip text={MINIMUM_OUTPUT_TOOLTIP}>
          {content()}
        </Tooltip>
      </Show>
    </span>
  )
}
