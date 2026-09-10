import type { Component } from 'solid-js'
import { createMemo } from 'solid-js'
import { formatTokenCount } from '../rendererUtils'
import { AnimatedCount } from './AnimatedCount'
import * as styles from './AnimatedCount.css'

export const ThinkingTokenCount: Component<{ tokens: number, paused?: boolean }> = (props) => {
  const display = createMemo(() => formatTokenCount(props.tokens, 2))
  return (
    <AnimatedCount
      display={display()}
      unit={display() === '1' ? 'token' : 'tokens'}
      rootClass={display() === '777' ? styles.starPower : undefined}
      paused={props.paused}
    />
  )
}
