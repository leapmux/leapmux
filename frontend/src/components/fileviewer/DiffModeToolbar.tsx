import type { Component } from 'solid-js'
import type { FileDiffBase, FileViewMode } from '~/stores/tab.store'
import { Show } from 'solid-js'
import * as styles from './DiffModeToolbar.css'

export interface DiffModeToolbarProps {
  mode: FileViewMode
  diffBase?: FileDiffBase
  hasStagedAndUnstaged?: boolean
  onModeChange: (mode: FileViewMode) => void
  onDiffBaseChange?: (base: FileDiffBase) => void
}

export const DiffModeToolbar: Component<DiffModeToolbarProps> = (props) => {
  const btn = (label: string, mode: FileViewMode, testId?: string) => (
    <button
      class={`${styles.toolbarButton}${props.mode === mode ? ` ${styles.toolbarButtonActive}` : ''}`}
      onClick={() => props.onModeChange(mode)}
      data-testid={testId}
    >
      {label}
    </button>
  )

  return (
    <div class={styles.toolbar} data-testid="diff-mode-toolbar">
      {btn('HEAD', 'head', 'diff-mode-head')}
      {btn('Working', 'working', 'diff-mode-working')}
      {btn('Unified', 'unified-diff', 'diff-mode-unified')}
      {btn('Split', 'split-diff', 'diff-mode-split')}
      <Show when={props.hasStagedAndUnstaged}>
        <div class={styles.separator} />
        <button
          class={`${styles.toolbarButton}${props.diffBase === 'head-vs-working' ? ` ${styles.toolbarButtonActive}` : ''}`}
          onClick={() => props.onDiffBaseChange?.('head-vs-working')}
        >
          vs Working
        </button>
        <button
          class={`${styles.toolbarButton}${props.diffBase === 'head-vs-staged' ? ` ${styles.toolbarButtonActive}` : ''}`}
          onClick={() => props.onDiffBaseChange?.('head-vs-staged')}
        >
          vs Staged
        </button>
      </Show>
    </div>
  )
}
