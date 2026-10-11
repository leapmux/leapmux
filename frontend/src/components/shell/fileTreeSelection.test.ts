import { batch, createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { followActiveTabSelection } from './fileTreeSelection'

/** The active tab as the rule reads it. A tab that is no file tab has the file path ''. */
interface ActiveTab {
  key: string
  workingDir: string
  filePath?: string
}

/** Run the selection rule over signals, and return their setters and the selection. */
function selection(initial: ActiveTab) {
  let api!: {
    setTabKey: (key: string) => void
    setWorkingDir: (dir: string) => void
    /** Make `tab` the active tab in one update, as one change of the active tab updates every value of it. */
    activate: (tab: ActiveTab) => void
    select: (path: string) => void
    selected: () => string
  }
  const dispose = createRoot((disposeRoot) => {
    const [tabKey, setTabKey] = createSignal(initial.key)
    const [workingDir, setWorkingDir] = createSignal(initial.workingDir)
    const [filePath, setFilePath] = createSignal(initial.filePath ?? '')
    const [selected, select] = createSignal('')
    followActiveTabSelection({ activeTabKey: tabKey, workingDir, activeFilePath: filePath, selection: selected, setSelection: select })
    const activate = (tab: ActiveTab) => batch(() => {
      setTabKey(tab.key)
      setWorkingDir(tab.workingDir)
      setFilePath(tab.filePath ?? '')
    })
    api = { setTabKey, setWorkingDir, activate, select, selected }
    return disposeRoot
  })
  return { ...api, dispose }
}

describe('followActiveTabSelection', () => {
  it('selects the working directory of the active tab', () => {
    const tree = selection({ key: 'agent:a1', workingDir: '/repo' })
    expect(tree.selected()).toBe('/repo')
    tree.dispose()
  })

  it('selects the working directory of each tab that becomes active, over the selection of the user', () => {
    const tree = selection({ key: 'agent:a1', workingDir: '/repo' })
    tree.select('/repo/package.json')
    tree.setWorkingDir('/other')
    tree.setTabKey('agent:a2')
    expect(tree.selected()).toBe('/other')
    tree.dispose()
  })

  // The working directory of a tab can resolve after the tab becomes active, for example when its agent reports.
  it('follows a working directory that changes on the same tab while the user selected nothing', () => {
    const tree = selection({ key: 'agent:a1', workingDir: '' })
    tree.setWorkingDir('/repo')
    expect(tree.selected()).toBe('/repo')
    tree.dispose()
  })

  // An update of the same tab, such as the status of its agent after startup, is no change of the active tab.
  it('keeps the selection of the user when the same tab updates its working directory', () => {
    const tree = selection({ key: 'agent:a1', workingDir: '/var/repo' })
    tree.select('/var/repo/package.json')
    tree.setWorkingDir('/private/var/repo')
    expect(tree.selected()).toBe('/var/repo/package.json')
    tree.dispose()
  })

  // A click on a file in the tree selects the file and then opens it in a file tab, which becomes active.
  it('keeps a selection that holds the file of a file tab that becomes active', () => {
    const tree = selection({ key: 'agent:a1', workingDir: '/repo' })
    tree.select('/repo/package.json')
    tree.activate({ key: 'file:f1', workingDir: '/repo', filePath: '/repo/package.json' })
    expect(tree.selected()).toBe('/repo/package.json')
    tree.dispose()
  })

  // The tree reveals the file of a file tab only through its Locate action, so a switch to that tab does not.
  it('selects the working directory of a file tab that becomes active while the selection holds another path', () => {
    const tree = selection({ key: 'agent:a1', workingDir: '/repo' })
    tree.select('/repo/src')
    tree.activate({ key: 'file:f1', workingDir: '/repo', filePath: '/repo/package.json' })
    expect(tree.selected()).toBe('/repo')
    tree.dispose()
  })

  it('keeps the file of the file tab over a later update of the same tab', () => {
    const tree = selection({ key: 'agent:a1', workingDir: '/repo' })
    tree.select('/repo/package.json')
    tree.activate({ key: 'file:f1', workingDir: '/repo', filePath: '/repo/package.json' })
    tree.setWorkingDir('/private/repo')
    expect(tree.selected()).toBe('/repo/package.json')
    tree.dispose()
  })

  it('selects the working directory of an agent tab that becomes active after a file tab', () => {
    const tree = selection({ key: 'file:f1', workingDir: '/repo', filePath: '/repo/package.json' })
    tree.select('/repo/package.json')
    tree.activate({ key: 'agent:a1', workingDir: '/repo' })
    expect(tree.selected()).toBe('/repo')
    tree.dispose()
  })

  it('selects nothing while the active tab has no working directory', () => {
    const tree = selection({ key: '', workingDir: '' })
    expect(tree.selected()).toBe('')
    tree.dispose()
  })
})
