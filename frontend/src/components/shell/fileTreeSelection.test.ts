import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { followActiveTabSelection } from './fileTreeSelection'

/** Run the selection rule over signals, and return their setters and the selection. */
function selection(initial: { tabKey: string, workingDir: string }) {
  let api!: {
    setTabKey: (key: string) => void
    setWorkingDir: (dir: string) => void
    select: (path: string) => void
    selected: () => string
  }
  const dispose = createRoot((disposeRoot) => {
    const [tabKey, setTabKey] = createSignal(initial.tabKey)
    const [workingDir, setWorkingDir] = createSignal(initial.workingDir)
    const [selected, select] = createSignal('')
    followActiveTabSelection({ activeTabKey: tabKey, workingDir, selection: selected, setSelection: select })
    api = { setTabKey, setWorkingDir, select, selected }
    return disposeRoot
  })
  return { ...api, dispose }
}

describe('followActiveTabSelection', () => {
  it('selects the working directory of the active tab', () => {
    const tree = selection({ tabKey: 'agent:a1', workingDir: '/repo' })
    expect(tree.selected()).toBe('/repo')
    tree.dispose()
  })

  it('selects the working directory of each tab that becomes active, over the selection of the user', () => {
    const tree = selection({ tabKey: 'agent:a1', workingDir: '/repo' })
    tree.select('/repo/package.json')
    tree.setWorkingDir('/other')
    tree.setTabKey('agent:a2')
    expect(tree.selected()).toBe('/other')
    tree.dispose()
  })

  // The working directory of a tab can resolve after the tab becomes active, for example when its agent reports.
  it('follows a working directory that changes on the same tab while the user selected nothing', () => {
    const tree = selection({ tabKey: 'agent:a1', workingDir: '' })
    tree.setWorkingDir('/repo')
    expect(tree.selected()).toBe('/repo')
    tree.dispose()
  })

  // An update of the same tab, such as the status of its agent after startup, is no change of the active tab.
  it('keeps the selection of the user when the same tab updates its working directory', () => {
    const tree = selection({ tabKey: 'agent:a1', workingDir: '/var/repo' })
    tree.select('/var/repo/package.json')
    tree.setWorkingDir('/private/var/repo')
    expect(tree.selected()).toBe('/var/repo/package.json')
    tree.dispose()
  })

  it('selects nothing while the active tab has no working directory', () => {
    const tree = selection({ tabKey: '', workingDir: '' })
    expect(tree.selected()).toBe('')
    tree.dispose()
  })
})
