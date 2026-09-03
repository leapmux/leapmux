import type { SidebarElementsOpts } from './SidebarElements'
import { describe, expect, it, vi } from 'vitest'
import { stubBranchRefActions } from '~/test-support/branchMenu'
import { buildCommonSidebarProps } from './SidebarElements'

/**
 * The sidebar prop bag must read NOTHING reactive while it is being built.
 *
 * `createLeftSidebarElement` is called from inside a reactive scope in
 * AppShell, so any reactive read performed during the build is tracked by that
 * scope — and the scope's response to a change is to produce a brand-new
 * `<LeftSidebar>`, i.e. a full remount. That tore down live DOM on every change
 * to the focused tab's context, the todo list, the worker list, or a turn
 * ending, which is how an open tree/branch context menu could be detached from
 * under a click ("element was detached from the DOM" in 065/159).
 *
 * Getters move each read to the moment the child component asks for the prop,
 * where it is tracked by that child instead. These tests pin the property that
 * makes that work: building touches no source, reading touches exactly one.
 */

/**
 * Wraps an opts object in a Proxy that records EVERY property read, so the
 * guard fails closed: a field added to SidebarElementsOpts and forwarded
 * eagerly is caught without anyone remembering to add a counting getter for it.
 *
 * `staticPassthroughs` is the allowlist of properties the build is ALLOWED to
 * touch while assembling the bag -- object identities and callbacks that carry
 * no reactive source, so reading them tracks nothing. Anything outside it is a
 * potential remount, which is what the test asserts on.
 */
function proxyOpts(opts: SidebarElementsOpts, touched: string[]): SidebarElementsOpts {
  return new Proxy(opts, {
    get(target, prop, receiver) {
      if (typeof prop === 'string')
        touched.push(prop)
      return Reflect.get(target, prop, receiver) as unknown
    },
  })
}

/**
 * Properties the prop-bag build may read directly. Each is a stable identity
 * (a store object, a callback) rather than a reactive source, so forwarding it
 * eagerly tracks nothing.
 */
const STATIC_PASSTHROUGHS = new Set([
  'mruEditorDeps',
  'sectionStore',
  'view',
  'selection',
  'loadSections',
  'onSelectWorkspace',
  'onNewWorkspace',
  'onRefreshWorkspaces',
  'onDeleteWorkspace',
  'onConfirmDelete',
  'onConfirmArchive',
  'onPostArchiveWorkspace',
  'getCurrentTabContext',
  'getMruAgentContext',
  'onFileSelect',
  'onFileOpen',
  'termOps',
  'gitStatusStore',
  'workerInfoFn',
  'channelStatusFn',
  'onAddTunnel',
  'onDeregisterWorker',
  'onRegisterWorker',
  'onTabClick',
  'getTileOrderForWorkspace',
  'tabItemOps',
  'branchActions',
])

/** Counts every reactive read so a test can assert the build performed none. */
function trackedOpts() {
  const reads: string[] = []
  const tabContext = vi.fn(() => {
    reads.push('getCurrentTabContext')
    return { workerId: 'w-1', workingDir: '/repo', homeDir: '/home/u' }
  })
  const noop = () => {}
  const opts = {
    mruEditorDeps: {},
    get workspaces() {
      reads.push('workspaces')
      return []
    },
    get activeWorkspaceId() {
      reads.push('activeWorkspaceId')
      return 'ws-1'
    },
    sectionStore: {},
    view: {},
    selection: {},
    loadSections: noop,
    onSelectWorkspace: noop,
    onNewWorkspace: noop,
    onRefreshWorkspaces: noop,
    onDeleteWorkspace: noop,
    onConfirmDelete: noop,
    onConfirmArchive: noop,
    onPostArchiveWorkspace: noop,
    getCurrentTabContext: tabContext,
    getMruAgentContext: () => ({ workingDir: '/repo', homeDir: '/home/u' }),
    get fileTreePath() {
      reads.push('fileTreePath')
      return '/repo'
    },
    onFileSelect: noop,
    onFileOpen: noop,
    get isActiveWorkspaceArchived() {
      reads.push('isActiveWorkspaceArchived')
      return false
    },
    get showTodos() {
      reads.push('showTodos')
      return false
    },
    get activeTodos() {
      reads.push('activeTodos')
      return []
    },
    termOps: { handleOpenTerminal: noop },
    gitStatusStore: {},
    get turnEndTrigger() {
      reads.push('turnEndTrigger')
      return 0
    },
    get activeTabReady() {
      reads.push('activeTabReady')
      return true
    },
    get activeFilePath() {
      reads.push('activeFilePath')
      return undefined
    },
    get hasActiveFileTab() {
      reads.push('hasActiveFileTab')
      return false
    },
    get workers() {
      reads.push('workers')
      return []
    },
    workerInfoFn: () => undefined,
    channelStatusFn: () => undefined,
    onAddTunnel: noop,
    onDeregisterWorker: noop,
    onRegisterWorker: noop,
    onTabClick: noop,
    getTileOrderForWorkspace: () => [],
    // A real bundle, not omitted: the pass-through assertion below compares
    // identities, and two `undefined`s compare equal without proving anything.
    branchActions: stubBranchRefActions(),
  } as unknown as SidebarElementsOpts
  return { opts, reads }
}

describe('buildCommonSidebarProps', () => {
  it('reads nothing reactive while building the prop bag', () => {
    const { opts, reads } = trackedOpts()

    buildCommonSidebarProps(opts, {
      isCollapsed: () => {
        reads.push('isCollapsed')
        return false
      },
      onExpand: () => {},
    })

    expect(reads).toEqual([])
  })

  it('forwards a pass-through prop it never names', () => {
    // The forward list is gone -- mergeProps carries every pass-through -- so
    // this is what pins that the mechanism is actually wired. It also covers
    // what the Proxy guard below no longer can: mergeProps reads descriptors
    // via ownKeys/getOwnPropertyDescriptor rather than `get`, so the guard sees
    // nothing and would pass even if the merge were removed entirely.
    const { opts } = trackedOpts()
    const props = buildCommonSidebarProps(opts)

    expect(props.branchActions).toBe((opts as { branchActions?: unknown }).branchActions)
    expect(props.gitStatusStore).toBe(opts.gitStatusStore)
    expect(props.workspaces).toEqual([])
  })

  it('lets a derived prop shadow the pass-through of the same name', () => {
    // mergeProps resolves later sources first, and the derived block is the
    // later source. Nothing collides today; this pins the direction so a
    // future derived getter cannot be silently overridden by `opts`.
    const { opts } = trackedOpts()
    const props = buildCommonSidebarProps(opts, {
      isCollapsed: () => true,
      onExpand: () => {},
    })

    expect(props.isCollapsed).toBe(true)
    expect(props.workerId).toBe('w-1')
  })

  it('touches no property outside the static allowlist while building', () => {
    // Fails CLOSED, unlike the assertion above: that one only sees the sources
    // trackedOpts was told to count, so a field added to SidebarElementsOpts
    // and forwarded eagerly would slip past it and remount the sidebar again.
    const touched: string[] = []
    const { opts } = trackedOpts()

    buildCommonSidebarProps(proxyOpts(opts, touched), {
      isCollapsed: () => false,
      onExpand: () => {},
    })

    const unexpected = [...new Set(touched)].filter(p => !STATIC_PASSTHROUGHS.has(p))
    expect(unexpected).toEqual([])
  })

  it('reads a source only when the corresponding prop is accessed', () => {
    const { opts, reads } = trackedOpts()
    const props = buildCommonSidebarProps(opts)

    expect(props.workerId).toBe('w-1')
    expect(reads).toEqual(['getCurrentTabContext'])

    expect(props.workspaces).toEqual([])
    expect(reads).toEqual(['getCurrentTabContext', 'workspaces'])
  })

  it('re-reads the tab context on every access rather than snapshotting it', () => {
    const contexts = [
      { workerId: 'w-1', workingDir: '/a', homeDir: '/home/u' },
      { workerId: 'w-2', workingDir: '/b', homeDir: '/home/u' },
    ]
    let current = 0
    const opts = {
      ...trackedOpts().opts,
      getCurrentTabContext: () => contexts[current],
    } as unknown as SidebarElementsOpts
    const props = buildCommonSidebarProps(opts)

    expect(props.workerId).toBe('w-1')
    expect(props.workingDir).toBe('/a')

    // The focused tab changes. Without getters the sidebar would have to be
    // rebuilt to see this — which is exactly the remount being removed.
    current = 1
    expect(props.workerId).toBe('w-2')
    expect(props.workingDir).toBe('/b')
  })

  it('re-evaluates the archived-workspace handlers instead of freezing them', () => {
    let archived = false
    const opts = {
      ...trackedOpts().opts,
      get isActiveWorkspaceArchived() { return archived },
    } as unknown as SidebarElementsOpts
    const props = buildCommonSidebarProps(opts)

    expect(props.onFileMention).toBeTypeOf('function')
    expect(props.onOpenTerminal).toBeTypeOf('function')

    archived = true
    expect(props.onFileMention).toBeUndefined()
    expect(props.onOpenTerminal).toBeUndefined()
  })

  it('hands back a stable handler reference across reads', () => {
    // Presence is reactive; identity must not be. A getter that minted a fresh
    // closure per read would look like a changed prop to anything keying off
    // it -- `<Show when={props.onOpenTerminal}>`, a memo -- and re-render on
    // every unrelated read.
    const { opts } = trackedOpts()
    const props = buildCommonSidebarProps(opts)

    expect(props.onFileMention).toBe(props.onFileMention)
    expect(props.onOpenTerminal).toBe(props.onOpenTerminal)
  })
})
