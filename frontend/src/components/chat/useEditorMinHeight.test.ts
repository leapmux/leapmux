import type { UseEditorMinHeightResult } from './useEditorMinHeight'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setStorageAccountForTests } from '~/lib/browserStorage'
import { TEST_USER_ID } from '~/test-support/crdtBridge'
import { resetEditorMinHeightCacheForTests, useEditorMinHeight } from './useEditorMinHeight'

// Every read is held open, so a test decides exactly when one lands. The hook's
// whole subject is what the composer shows WHILE a read is in flight, and a
// real round-trip settles too early to state that at all. The storage helper
// itself is covered by `~/lib/editor/editorMinHeight.test.ts`.
const storage = vi.hoisted(() => ({
  reads: [] as { agentId: string, resolve: (height: number | undefined) => void }[],
}))

vi.mock('~/lib/editor/editorMinHeight', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/lib/editor/editorMinHeight')>()
  return {
    ...actual,
    getStoredEditorMinHeight: (agentId: string) => new Promise<number | undefined>((resolve) => {
      storage.reads.push({ agentId, resolve })
    }),
    persistEditorMinHeight: vi.fn(),
    clearEditorMinHeight: vi.fn(),
  }
})

const OTHER_ACCOUNT = 'other-account'

beforeEach(() => {
  storage.reads = []
  // The suite's global hook clears every account listener, which takes this
  // module's import-time subscription with it.
  resetEditorMinHeightCacheForTests()
})

afterEach(() => {
  setStorageAccountForTests(TEST_USER_ID)
})

/** Mount the hook on `agentId`, with a container tall enough to be irrelevant. */
function mount(agentId?: string) {
  const [id, setAgentId] = createSignal<string | undefined>(agentId)
  let hook!: UseEditorMinHeightResult
  let dispose!: () => void
  createRoot((d) => {
    dispose = d
    hook = useEditorMinHeight({
      agentId: id,
      containerHeight: () => 800,
      panelRef: () => undefined,
    })
  })
  return { setAgentId, hook, dispose }
}

/** Let the effect run and any resolved read apply. */
async function settle(): Promise<void> {
  for (let i = 0; i < 4; i++)
    await Promise.resolve()
}

/** Land the outstanding read for `agentId`. */
async function land(agentId: string, height: number | undefined): Promise<void> {
  const read = storage.reads.find(r => r.agentId === agentId)
  if (read === undefined)
    throw new Error(`no read is outstanding for ${agentId}`)
  storage.reads = storage.reads.filter(r => r !== read)
  read.resolve(height)
  await settle()
}

describe('useEditorMinHeight', () => {
  it('publishes the stored height once the read lands', async () => {
    const h = mount('agent-a')
    await settle()
    expect(h.hook.editorMinHeight()).toBeUndefined()

    await land('agent-a', 220)

    expect(h.hook.editorMinHeight()).toBe(220)
    h.dispose()
  })

  // The cache is what keeps a switch back to a visited agent from flashing the
  // default height, and what stops a rapid switch issuing a read per frame.
  it('answers a revisited agent from the cache, with no second read', async () => {
    const h = mount('agent-a')
    await settle()
    await land('agent-a', 220)

    h.setAgentId('agent-b')
    await settle()
    await land('agent-b', 300)
    expect(h.hook.editorMinHeight()).toBe(300)

    h.setAgentId('agent-a')
    await settle()
    expect(h.hook.editorMinHeight()).toBe(220)
    expect(storage.reads).toEqual([])
    h.dispose()
  })

  // A MISS PUBLISHES NOTHING until the read lands. Writing `undefined` first
  // collapses the composer to its one-line default and expands it again a round
  // trip later, for every agent the session has not visited yet.
  it('holds the height on screen while an unvisited agent is read', async () => {
    const h = mount('agent-a')
    await settle()
    await land('agent-a', 220)

    h.setAgentId('agent-b')
    await settle()

    expect(h.hook.editorMinHeight()).toBe(220)
    await land('agent-b', 300)
    expect(h.hook.editorMinHeight()).toBe(300)
    h.dispose()
  })

  // A slow read must not paint an agent that is no longer on screen. The two
  // reads land in the WRONG order here, which is the case a plain `then` misses.
  it('ignores a read that lands after the agent moved on', async () => {
    const h = mount('agent-a')
    await settle()
    h.setAgentId('agent-b')
    await settle()

    await land('agent-b', 300)
    await land('agent-a', 220)

    expect(h.hook.editorMinHeight()).toBe(300)
    h.dispose()
  })

  // `PREFIX_EDITOR_MIN_HEIGHT` is account-scoped, and an in-tab account switch
  // needs no reload -- `AuthContext` moves the namespace in place. Without the
  // subscription the second account serves the first account's heights for
  // every agent id the two happen to share, and saves them back under its own
  // namespace on the next resize.
  it('drops the cache when the storage account moves, so the next account re-reads', async () => {
    const first = mount('agent-a')
    await settle()
    await land('agent-a', 220)
    first.dispose()

    setStorageAccountForTests(OTHER_ACCOUNT)

    const second = mount('agent-a')
    await settle()
    // A cache hit would have answered 220 synchronously and issued no read.
    expect(second.hook.editorMinHeight()).toBeUndefined()
    await land('agent-a', 140)

    expect(second.hook.editorMinHeight()).toBe(140)
    second.dispose()
  })

  it('reads nothing while there is no agent', async () => {
    const h = mount(undefined)
    await settle()

    expect(storage.reads).toEqual([])
    expect(h.hook.editorMinHeight()).toBeUndefined()
    h.dispose()
  })
})
