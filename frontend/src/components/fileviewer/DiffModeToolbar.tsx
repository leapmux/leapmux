import type { Component } from 'solid-js'
import type { FileDiffBase, FileViewMode } from '~/stores/tab.types'
import { Show } from 'solid-js'
import * as styles from './DiffModeToolbar.css'

export interface DiffModeToolbarProps {
  mode: FileViewMode
  diffBase?: FileDiffBase
  hasStagedAndUnstaged?: boolean
  /**
   * Whether a diff is available for this file. When false, the
   * Unified / Split buttons and the head-vs-working / head-vs-staged
   * sub-toggle are hidden — a file with no git-status entry has no
   * diff to render, so offering the modes would only mislead. HEAD /
   * Working buttons stay since they navigate between revisions
   * independently of diff state.
   */
  diffAvailable?: boolean
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
    <div class={styles.toolbarWrapper}>
      <div class={styles.toolbar} data-testid="diff-mode-toolbar">
        {btn('HEAD', 'head', 'diff-mode-head')}
        {btn('Working', 'working', 'diff-mode-working')}
        <Show when={props.diffAvailable}>
          {btn('Unified', 'unified-diff', 'diff-mode-unified')}
          {btn('Split', 'split-diff', 'diff-mode-split')}
        </Show>
        <Show when={props.diffAvailable && props.hasStagedAndUnstaged}>
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
    </div>
  )
}
