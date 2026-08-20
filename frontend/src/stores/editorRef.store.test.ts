import type { EditorRef } from './editorRef.store'
import type { Tab } from '~/stores/tab.types'
import { describe, expect, it, vi } from 'vitest'
import { registerProvider } from '~/components/chat/providers/registry'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { computeSeparator, getEditorRef, insertIntoAgentEditor, insertIntoMruAgentEditor, registerEditorRef, unregisterEditorRef } from './editorRef.store'

/** An EditorRef with the parts a test does not care about filled in. */
function makeRef(over: Partial<EditorRef> = {}): EditorRef {
  // `get` reads back what `set` wrote, rather than a constant ''. The flush in
  // registerEditorRef retries every 50 ms while `get()` reads empty, so a fake
  // that never reflects the write keeps firing timers for ~500 ms after the test
  // that armed it finished -- into whichever test runs next in this module.
  let content = ''
  const ref: EditorRef = {
    get: () => content,
    set: (value) => { content = value },
    focus: vi.fn(),
    insert: vi.fn(),
    writable: () => true,
  }
  // A `set` override still records; keep the content mirror behind it so `get`
  // stays consistent for the retry check.
  if (over.set) {
    const overridden = over.set
    over = {
      ...over,
      set: (value) => {
        content = value
        overridden(value)
      },
    }
  }
  return { ...ref, ...over }
}

const writableRef = (over: Partial<EditorRef> = {}): EditorRef => makeRef(over)
const readOnlyRef = (over: Partial<EditorRef> = {}): EditorRef => makeRef({ ...over, writable: () => false })

describe('computeSeparator', () => {
  it('returns empty string when current is empty (block)', () => {
    expect(computeSeparator('', 'block')).toBe('')
  })

  it('returns empty string when current is empty (inline)', () => {
    expect(computeSeparator('', 'inline')).toBe('')
  })

  it('returns \\n\\n for block mode with existing content', () => {
    expect(computeSeparator('hello', 'block')).toBe('\n\n')
  })

  it('returns space for inline mode with existing content', () => {
    expect(computeSeparator('hello', 'inline')).toBe(' ')
  })

  it('returns empty string for inline mode when current ends with newline', () => {
    expect(computeSeparator('hello\n', 'inline')).toBe('')
  })

  it('returns \\n\\n for block mode even when current ends with newline', () => {
    expect(computeSeparator('hello\n', 'block')).toBe('\n\n')
  })
})

/**
 * The AGENT narrowing lives here and nowhere else: `mruAgentEditorDeps` hands
 * over every tab type in MRU order. Nothing covered it — every case in
 * `mruAgentEditorDeps.test.ts` adds only agents, so deleting the filter left
 * the whole suite green while "quote to agent" would target whatever tab the
 * user last touched.
 */
describe('insertIntoMruAgentEditor', () => {
  const agent = (id: string): Tab => ({ type: TabType.AGENT, id, workspaceId: 'ws' })
  const terminal = (id: string): Tab => ({ type: TabType.TERMINAL, id, workspaceId: 'ws' })

  it('skips a non-agent tab at the MRU head and reaches the agent behind it', () => {
    const set = vi.fn()
    const activate = vi.fn()
    registerEditorRef('a1', writableRef({ get: () => '', set }))
    try {
      insertIntoMruAgentEditor(
        // A terminal is the most recently touched tab; the agent is behind it.
        { mruTabs: () => [terminal('t1'), agent('a1')], activate },
        'hello',
      )
      expect(set).toHaveBeenCalled()
      expect(activate, 'a terminal must never be an editor target')
        .not
        .toHaveBeenCalledWith(expect.objectContaining({ id: 't1' }))
    }
    finally {
      unregisterEditorRef('a1')
    }
  })

  it('does nothing when the workspace holds no agent at all', () => {
    const activate = vi.fn()
    insertIntoMruAgentEditor({ mruTabs: () => [terminal('t1')], activate }, 'hello')
    expect(activate).not.toHaveBeenCalled()
  })

  it('skips a non-steerable child agent and reaches the steerable root behind it', () => {
    const setRoot = vi.fn()
    const activate = vi.fn()
    // A non-steerable child: parentAgentId set, acceptsMessages false, non-Codex
    // provider. Its composer is disabled and it must never receive an inserted
    // mention or quote.
    const nonSteerableChild: Tab = {
      type: TabType.AGENT,
      id: 'c1',
      workspaceId: 'ws',
      parentAgentId: 'root-1',
      acceptsMessages: false,
      agentProvider: AgentProvider.CLAUDE_CODE,
    }
    // The root is always steerable (no parentAgentId).
    const root: Tab = { type: TabType.AGENT, id: 'root-1', workspaceId: 'ws' }
    registerEditorRef('root-1', writableRef({ get: () => '', set: setRoot }))
    try {
      insertIntoMruAgentEditor(
        // Non-steerable child is MRU; the root is behind it.
        { mruTabs: () => [nonSteerableChild, root], activate },
        'hello',
      )
      expect(setRoot, 'the steerable root must receive the text').toHaveBeenCalled()
      expect(activate, 'a non-steerable child must never be targeted')
        .not
        .toHaveBeenCalledWith(expect.objectContaining({ id: 'c1' }))
    }
    finally {
      unregisterEditorRef('root-1')
    }
  })

  it('targets a steerable child (Codex, accepts messages) directly', () => {
    const setChild = vi.fn()
    const activate = vi.fn()
    // A steerable child: Codex child that accepts messages.
    const steerableChild: Tab = {
      type: TabType.AGENT,
      id: 'c1',
      workspaceId: 'ws',
      parentAgentId: 'root-1',
      acceptsMessages: true,
      agentProvider: AgentProvider.CODEX,
    }
    registerEditorRef('c1', writableRef({ get: () => '', set: setChild }))
    try {
      insertIntoMruAgentEditor(
        { mruTabs: () => [steerableChild], activate },
        'hello',
      )
      expect(setChild, 'a steerable child receives the text').toHaveBeenCalled()
      expect(activate).toHaveBeenCalledWith(expect.objectContaining({ id: 'c1' }))
    }
    finally {
      unregisterEditorRef('c1')
    }
  })

  it('optimistically targets a Codex child before hydration (acceptsMessages undefined)', () => {
    const setChild = vi.fn()
    const activate = vi.fn()
    // A Codex child whose acceptsMessages is not yet known (before listAgents
    // hydration). isSteerableAgentTab routes the pre-hydration fallback through
    // the provider plugin's supportsSubagentSend, so register Codex's capability.
    registerProvider(AgentProvider.CODEX, { classify: () => ({} as never), supportsSubagentSend: true })
    const codexChildUnhydrated: Tab = {
      type: TabType.AGENT,
      id: 'c1',
      workspaceId: 'ws',
      parentAgentId: 'root-1',
      agentProvider: AgentProvider.CODEX,
    }
    registerEditorRef('c1', writableRef({ get: () => '', set: setChild }))
    try {
      insertIntoMruAgentEditor(
        { mruTabs: () => [codexChildUnhydrated], activate },
        'hello',
      )
      expect(setChild, 'a Codex child is optimistically steerable pre-hydration').toHaveBeenCalled()
    }
    finally {
      unregisterEditorRef('c1')
    }
  })
})

/**
 * A read-only composer is MOUNTED and registered like any other -- a
 * non-steerable subagent's editor is disabled, not absent -- so the registry is
 * the only place that can refuse a write to it. Refusing here rather than at
 * each call site is what covers the writers that never call a function in this
 * module (type-to-focus uses the ref directly) and the ones not written yet.
 */
describe('a read-only editor refuses writes', () => {
  it('insertIntoAgentEditor writes nothing and reports refused', () => {
    const set = vi.fn()
    registerEditorRef('ro', readOnlyRef({ get: () => 'draft', set }))
    try {
      expect(insertIntoAgentEditor('ro', 'quoted')).toBe('refused')
      expect(set).not.toHaveBeenCalled()
    }
    finally {
      unregisterEditorRef('ro')
    }
  })

  it('insertIntoAgentEditor reports inserted for a writable editor', () => {
    const set = vi.fn()
    registerEditorRef('rw', writableRef({ get: () => 'draft', set }))
    try {
      expect(insertIntoAgentEditor('rw', 'quoted')).toBe('inserted')
      expect(set).toHaveBeenCalledWith('draft\n\nquoted')
    }
    finally {
      unregisterEditorRef('rw')
    }
  })

  // `queued` and `refused` are separate answers on purpose. Merging them into
  // one boolean is what let the quote handler read a refusing editor as an
  // absent one and park text nothing would ever flush.
  it('insertIntoAgentEditor reports queued when no editor is registered', () => {
    vi.useFakeTimers()
    const set = vi.fn()
    try {
      expect(insertIntoAgentEditor('missing', 'quoted')).toBe('queued')
      registerEditorRef('missing', writableRef({ set }))
      vi.advanceTimersByTime(100)
      expect(set).toHaveBeenCalledWith('quoted')
    }
    finally {
      unregisterEditorRef('missing')
      vi.useRealTimers()
    }
  })

  it('getEditorRef hands out a handle whose set and insert are inert', () => {
    const set = vi.fn()
    const insert = vi.fn()
    registerEditorRef('ro', readOnlyRef({ get: () => 'draft', set, insert }))
    try {
      const ref = getEditorRef('ro')
      ref?.set('replaced')
      ref?.insert('x')
      expect(set).not.toHaveBeenCalled()
      expect(insert).not.toHaveBeenCalled()
      // Reading and focusing stay available: the transcript is still readable,
      // and focus moves without changing anything.
      expect(ref?.get()).toBe('draft')
    }
    finally {
      unregisterEditorRef('ro')
    }
  })

  it('getEditorRef still forwards set and insert for a writable editor', () => {
    const set = vi.fn()
    const insert = vi.fn()
    registerEditorRef('rw', writableRef({ set, insert }))
    try {
      const ref = getEditorRef('rw')
      ref?.set('replaced')
      ref?.insert('x')
      expect(set).toHaveBeenCalledWith('replaced')
      expect(insert).toHaveBeenCalledWith('x')
    }
    finally {
      unregisterEditorRef('rw')
    }
  })

  // Writability is unknown when a queue is built, because the editor is not
  // mounted yet. Flushing it into an editor that turns out to be read-only would
  // land the text late -- and the draft layer would then persist it under that
  // subagent's own key, where it survives a reload.
  it('drops a queued insert when the editor registers read-only', async () => {
    const set = vi.fn()
    const activate = vi.fn()
    insertIntoMruAgentEditor(
      { mruTabs: () => [{ type: TabType.AGENT, id: 'later', workspaceId: 'ws' } as Tab], activate },
      'queued',
    )
    registerEditorRef('later', readOnlyRef({ set }))
    try {
      // The flush is scheduled on a timer, so give it more than its own delay.
      await new Promise(resolve => setTimeout(resolve, 120))
      expect(set).not.toHaveBeenCalled()
    }
    finally {
      unregisterEditorRef('later')
    }
  })

  // Dropping is right; dropping SILENTLY is not. The destination is chosen from
  // optimistic tab state -- a subagent tab opened from the sidebar has no
  // parentAgentId until listAgents hydrates it, so it looks steerable and wins
  // the MRU until its composer mounts and says otherwise. The text goes to the
  // nearest agent that accepts messages, which is the same answer this function
  // gives when the target is read-only up front.
  it('re-routes a dropped queue to the nearest writable agent', async () => {
    const setRoot = vi.fn()
    const notify = vi.fn()
    const optimisticChild = { type: TabType.AGENT, id: 'c1', workspaceId: 'ws' } as Tab
    const hydratedChild = {
      type: TabType.AGENT,
      id: 'c1',
      workspaceId: 'ws',
      parentAgentId: 'root-1',
      acceptsMessages: false,
    } as Tab
    const root = { type: TabType.AGENT, id: 'root-1', workspaceId: 'ws' } as Tab
    let hydrated = false
    const deps = {
      mruTabs: () => (hydrated ? [hydratedChild, root] : [optimisticChild, root]),
      activate: vi.fn(),
      notify,
    }
    registerEditorRef('root-1', writableRef({ set: setRoot }))
    try {
      insertIntoMruAgentEditor(deps, 'quoted')
      // listAgents lands and the child's composer mounts read-only.
      hydrated = true
      registerEditorRef('c1', readOnlyRef())
      await new Promise(resolve => setTimeout(resolve, 120))

      expect(setRoot).toHaveBeenCalledWith('quoted')
      expect(notify).toHaveBeenCalledOnce()
    }
    finally {
      unregisterEditorRef('c1')
      unregisterEditorRef('root-1')
    }
  })

  // The other silent drop on this path: the MRU target is MOUNTED and refuses
  // input, so the text is not queued either -- nothing would ever flush a queue
  // for an editor that already registered. The user has to be told, or the
  // quote disappears with the tab moving forward as if it had landed.
  it('reports a refused insert instead of dropping it silently', () => {
    const notify = vi.fn()
    const activate = vi.fn()
    const target = { type: TabType.AGENT, id: 'ro-target', workspaceId: 'ws' } as Tab
    registerEditorRef('ro-target', readOnlyRef())
    try {
      insertIntoMruAgentEditor({ mruTabs: () => [target], activate, notify }, 'quoted')
      expect(notify).toHaveBeenCalledOnce()
      expect(activate).toHaveBeenCalledWith(target)
    }
    finally {
      unregisterEditorRef('ro-target')
    }
  })

  it('still flushes a queued insert when the editor registers writable', async () => {
    const set = vi.fn()
    const activate = vi.fn()
    insertIntoMruAgentEditor(
      { mruTabs: () => [{ type: TabType.AGENT, id: 'later2', workspaceId: 'ws' } as Tab], activate },
      'queued',
    )
    registerEditorRef('later2', writableRef({ set }))
    try {
      await new Promise(resolve => setTimeout(resolve, 120))
      expect(set).toHaveBeenCalledWith('queued')
    }
    finally {
      unregisterEditorRef('later2')
    }
  })
})

/**
 * Queueing is only correct when the editor is ABSENT. A mounted editor already
 * registered, so nothing will ever flush a queue for it -- text parked
 * there would land late on some later re-registration.
 */
describe('insertIntoMruAgentEditor with a mounted read-only editor', () => {
  it('drops the text rather than queueing it', async () => {
    const set = vi.fn()
    const activate = vi.fn()
    // A root tab (always steerable by tab state) whose live editor refuses
    // input: the two sources disagree, and the editor is the one that knows.
    registerEditorRef('root-1', readOnlyRef({ set }))
    try {
      insertIntoMruAgentEditor(
        { mruTabs: () => [{ type: TabType.AGENT, id: 'root-1', workspaceId: 'ws' } as Tab], activate },
        'hello',
      )
      expect(set).not.toHaveBeenCalled()
      // Nothing was parked: re-registering as writable must not replay it.
      unregisterEditorRef('root-1')
      const laterSet = vi.fn()
      registerEditorRef('root-1', writableRef({ set: laterSet }))
      await new Promise(resolve => setTimeout(resolve, 120))
      expect(laterSet, 'a dropped insert must not resurface later').not.toHaveBeenCalled()
    }
    finally {
      unregisterEditorRef('root-1')
    }
  })
})

/**
 * `writable` is a thunk, not a boolean, because a subagent tab answers from its
 * provider plugin until the worker's authoritative `acceptsMessages` arrives --
 * so the answer flips under a ref that was registered once. Reading it per call
 * is what makes the guard follow that change.
 */
describe('writable is re-read on every write', () => {
  it('follows a ref that becomes writable after registration', () => {
    const set = vi.fn()
    let writable = false
    registerEditorRef('flip', makeRef({ get: () => '', set, writable: () => writable }))
    try {
      expect(insertIntoAgentEditor('flip', 'first')).toBe('refused')
      writable = true
      expect(insertIntoAgentEditor('flip', 'second')).toBe('inserted')
      expect(set).toHaveBeenCalledTimes(1)
      expect(set).toHaveBeenCalledWith('second')
    }
    finally {
      unregisterEditorRef('flip')
    }
  })

  // The flush retries for ~500 ms after registration, and the worker's
  // authoritative acceptsMessages can arrive inside that window. Checking
  // writability once, at registration, let the later attempts write into a
  // composer that already turned read-only -- where the draft layer persists
  // the text under that subagent's key and it survives a reload.
  it('stops a queued flush when the editor turns read-only mid-retry', async () => {
    const set = vi.fn()
    const activate = vi.fn()
    let writable = true
    insertIntoMruAgentEditor(
      { mruTabs: () => [{ type: TabType.AGENT, id: 'flip-flush', workspaceId: 'ws' } as Tab], activate },
      'queued',
    )
    registerEditorRef('flip-flush', makeRef({ get: () => '', set, writable: () => writable }))
    try {
      writable = false
      await new Promise(resolve => setTimeout(resolve, 200))
      expect(set).not.toHaveBeenCalled()
    }
    finally {
      unregisterEditorRef('flip-flush')
    }
  })

  // A tab can close inside the same retry window. Without re-reading the
  // registry the loop kept calling set() and finally focus() on an unmounted
  // composer, stealing focus from whatever replaced it.
  it('stops a queued flush when the editor unregisters mid-retry', async () => {
    const set = vi.fn()
    const focus = vi.fn()
    const activate = vi.fn()
    insertIntoMruAgentEditor(
      { mruTabs: () => [{ type: TabType.AGENT, id: 'gone-flush', workspaceId: 'ws' } as Tab], activate },
      'queued',
    )
    // `get` stays empty so the flush would retry to its 10-attempt limit.
    registerEditorRef('gone-flush', makeRef({ get: () => '', set, focus }))
    unregisterEditorRef('gone-flush')
    await new Promise(resolve => setTimeout(resolve, 200))
    expect(set).not.toHaveBeenCalled()
    expect(focus).not.toHaveBeenCalled()
  })

  it('follows a ref that becomes read-only after registration', () => {
    const insert = vi.fn()
    let writable = true
    registerEditorRef('flip', makeRef({ insert, writable: () => writable }))
    try {
      getEditorRef('flip')?.insert('a')
      writable = false
      // The SAME handle, taken while the editor still accepted input, must stop
      // writing once it does not -- a caller can hold one across the change.
      const held = getEditorRef('flip')
      held?.insert('b')
      expect(insert).toHaveBeenCalledTimes(1)
      expect(insert).toHaveBeenCalledWith('a')
    }
    finally {
      unregisterEditorRef('flip')
    }
  })
})
