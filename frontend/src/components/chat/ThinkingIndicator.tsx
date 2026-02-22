import type { Component } from 'solid-js'
import * as styles from './ThinkingIndicator.css'

export const ThinkingIndicator: Component = () => (
  <div class={styles.container} data-testid="thinking-indicator">
    <span class={styles.dot} />
    <span class={styles.dot} style={{ 'animation-delay': '0.3s' }} />
    <span class={styles.dot} style={{ 'animation-delay': '0.6s' }} />
  </div>
)
