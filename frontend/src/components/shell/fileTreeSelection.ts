import { createEffect, untrack } from 'solid-js'

/** What {@link followActiveTabSelection} reads and writes. */
export interface FileTreeSelectionOpts {
  /** The identity of the active tab, or '' while none is active. */
  activeTabKey: () => string
  /** The working directory of the active tab, or '' while it is unknown. */
  workingDir: () => string
  /** The path that the Files tree selects. */
  selection: () => string
  setSelection: (path: string) => void
}

/**
 * Select the working directory of the active tab in the Files tree. Run it inside a Solid root.
 *
 * A tab that becomes active resets the selection to its working directory. An update of the same tab is no change of
 * the active tab: the status of its agent, its title, or a working directory that resolves later all update it. Such an
 * update moves the selection only while the user has not moved it, so a late working directory still shows, and a
 * file that the user selected stays selected.
 */
export function followActiveTabSelection(opts: FileTreeSelectionOpts): void {
  // The active tab when the effect last ran, and the selection that this rule wrote for it.
  let tabKey: string | undefined
  let ruleSelection = ''
  createEffect(() => {
    const key = opts.activeTabKey()
    const workingDir = opts.workingDir()
    untrack(() => {
      if (key === tabKey && opts.selection() !== ruleSelection)
        return
      tabKey = key
      ruleSelection = workingDir
      opts.setSelection(workingDir)
    })
  })
}
