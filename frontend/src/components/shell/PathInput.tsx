import type { Component } from 'solid-js'
import type { PathFlavor } from '~/lib/paths'
import { createEffect, createMemo, createSignal, Show } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { detectFlavor, isAbsolute, tildify, untildify } from '~/lib/paths'
import * as styles from './PathInput.css'

export interface PathInputProps {
  /** The path to display, in the worker's flavor. Shown tildified. */
  selectedPath: string
  /** The worker's home directory, used to abbreviate and expand `~`. */
  homeDir?: string
  /** The worker's path flavor, which decides what an absolute path looks like. */
  flavor: PathFlavor
  /** Called with an expanded, worker-flavored path on Enter or on blur. */
  onSubmit: (path: string) => void
}

/**
 * The "type a path" box above a directory picker.
 *
 * It belongs to the PICKER, where choosing a directory IS the task. The sidebar
 * hosted the same box until this component was extracted, bound to the tree's
 * SELECTED path -- typing one jumped the tree there. That capability was
 * dropped deliberately along with the box; the sidebar now navigates by
 * clicking and by "Locate active file". Do not describe the sidebar's box as
 * having led nowhere: it worked, and the tree still scrolls to whatever
 * `selectedPath` becomes, which is how this box drives the picker's tree.
 */
export const PathInput: Component<PathInputProps> = (props) => {
  const [inputValue, setInputValue] = createSignal('')

  // Sync the external selection into the box, tildified for display.
  createEffect(() => {
    setInputValue(tildify(props.selectedPath, props.homeDir, props.flavor))
  })

  const submitPath = (raw: string) => {
    const value = raw.trim()
    if (!value)
      return
    props.onSubmit(untildify(value, props.homeDir, props.flavor))
  }

  const handleKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      submitPath(inputValue())
    }
  }

  const handleBlur = () => {
    const value = inputValue().trim()
    if (!value)
      return
    // Avoid re-emitting the displayed tildified value.
    if (value === tildify(props.selectedPath, props.homeDir, props.flavor))
      return
    submitPath(value)
  }

  const flavorHint = createMemo(() => {
    const raw = inputValue().trim()
    if (!raw || raw.startsWith('~'))
      return null
    const rawFlavor = detectFlavor(raw)
    if (!isAbsolute(raw, rawFlavor))
      return null
    if (rawFlavor === props.flavor)
      return null
    return props.flavor === 'win32'
      ? 'This looks like a POSIX path but the worker expects Windows paths.'
      : 'This looks like a Windows path but the worker expects POSIX paths.'
  })

  return (
    <>
      <div class={styles.pathInput}>
        <Tooltip text={props.selectedPath} showWhen="clipped">
          <input
            type="text"
            value={inputValue()}
            onInput={e => setInputValue(e.currentTarget.value)}
            // Direct listener so preventDefault() fires before Dialog's keydown handler.
            on:keydown={handleKeyDown}
            onBlur={handleBlur}
            placeholder="Enter path..."
          />
        </Tooltip>
      </div>
      <Show when={flavorHint()}>
        {hint => (
          <div class={styles.pathHint} data-testid="path-flavor-hint">{hint()}</div>
        )}
      </Show>
    </>
  )
}
