import type { UserEventsSubscription } from './crdt'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TabType } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { seedTabIntoWorkspace } from './crdt'

const hubUrl = 'http://crdt-hub.test'

/** A subscription that already knows the root node of the workspace. */
const userEvents: UserEventsSubscription = {
  awaitRootNodeId: async () => 'root-node',
  currentEpoch: () => 7n,
  isClosed: () => false,
  close: () => {},
}

/** Answer each hub request by its RPC, and record the RPCs in order. */
function hub(answer: (method: string, body: unknown) => Response) {
  const methods: string[] = []
  const bodies: unknown[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const method = String(input).split('.v1.').at(-1) ?? ''
    const body: unknown = JSON.parse(String(init?.body))
    methods.push(method)
    bodies.push(body)
    return answer(method, body)
  }))
  return { methods, bodies }
}

function seed() {
  return seedTabIntoWorkspace({ hubUrl, cookie: 'leapmux-session=s', workspaceId: 'ws-1', tabType: TabType.AGENT, tabId: 'agent-1', workerId: 'worker-1', userEvents })
}

const committed = () => Response.json({ results: [{ committed: {} }] })

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('seedTabIntoWorkspace', () => {
  it('submits the tile, position, and Worker registers, and returns once ListTabs shows the tab', async () => {
    const { methods, bodies } = hub(method => method === 'UserCRDT/SubmitOps' ? committed() : Response.json({ tabs: [{ tabId: 'agent-1' }] }))
    await seed()
    expect(methods).toEqual(['UserCRDT/SubmitOps', 'WorkspaceService/ListTabs'])
    expect(bodies[0]).toMatchObject({
      epoch: '7',
      batches: [{ ops: [
        { setTabRegister: { tabType: TabType.AGENT, tabId: 'agent-1', tileId: 'root-node' } },
        { setTabRegister: { tabType: TabType.AGENT, tabId: 'agent-1', position: 'M' } },
        { setTabRegister: { tabType: TabType.AGENT, tabId: 'agent-1', workerId: 'worker-1' } },
      ] }],
    })
    expect(bodies[1]).toEqual({ workspaceIds: ['ws-1'] })
  })

  it('reads ListTabs again until the tab shows', async () => {
    vi.useFakeTimers()
    let reads = 0
    const { methods } = hub(method => method === 'UserCRDT/SubmitOps' ? committed() : Response.json({ tabs: ++reads < 3 ? [] : [{ tabId: 'agent-1' }] }))
    const seeding = seed()
    await vi.advanceTimersByTimeAsync(1_000)
    await expect(seeding).resolves.toBeUndefined()
    expect(methods.filter(method => method === 'WorkspaceService/ListTabs')).toHaveLength(3)
  })

  it('states the hub reason of a refused ListTabs read when the tab never shows', async () => {
    vi.useFakeTimers()
    hub(method => method === 'UserCRDT/SubmitOps' ? committed() : new Response('{"code":"unavailable","message":"projection rebuilds"}', { status: 503 }))
    const outcome = seed().then(() => null, (error: unknown) => error)
    await vi.advanceTimersByTimeAsync(5_000)
    const failure = await outcome
    expect(failure).toBeInstanceOf(Error)
    expect((failure as Error).message).toContain('tab agent-1 not visible via ListTabs')
    expect((failure as Error).message).toContain('projection rebuilds')
  })

  it('states the hub reason and the batch of a refused SubmitOps, and reads no tab list', async () => {
    const { methods } = hub(() => new Response('{"code":"failed_precondition","message":"stale epoch"}', { status: 400 }))
    await expect(seed()).rejects.toThrow(/UserCRDT\/SubmitOps returned HTTP 400: .*stale epoch/)
    expect(methods).toEqual(['UserCRDT/SubmitOps'])
  })

  it('refuses a batch that the hub rejected or did not commit', async () => {
    hub(() => Response.json({ results: [{ rejected: { reason: 'conflict' } }] }))
    await expect(seed()).rejects.toThrow('SubmitOps batch rejected: {"reason":"conflict"}')
    hub(() => Response.json({ results: [{}] }))
    await expect(seed()).rejects.toThrow('SubmitOps batch had no committed result')
  })
})
