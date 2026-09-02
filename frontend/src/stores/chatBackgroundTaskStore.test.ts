import type { BackgroundTaskItem as ProtoBackgroundTaskItem } from '~/generated/proto/leapmux/v1/agent_pb'
import { describe, expect, it } from 'vitest'
import { BackgroundTaskKind, BackgroundTaskStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { createBackgroundTaskStore } from '~/stores/chatBackgroundTaskStore'

function proto(id: string, status: BackgroundTaskStatus, activeForm = ''): ProtoBackgroundTaskItem {
  return {
    id,
    kind: BackgroundTaskKind.SUBAGENT,
    status,
    title: id,
    activeForm,
    childAgentId: '',
    parentAgentId: '',
    groupKey: '',
    groupLabel: '',
    description: '',
    createdAt: '',
    updatedAt: '',
    endedAt: '',
  } as ProtoBackgroundTaskItem
}

describe('createBackgroundTaskStore', () => {
  it('replace applies the proto snapshot', () => {
    const store = createBackgroundTaskStore()
    store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING, 'working')])
    expect(store.get('a1')).toHaveLength(1)
    expect(store.get('a1')[0].status).toBe('running')
    expect(store.get('a1')[0].activity).toBe('working')
  })

  // The registry arrives WHOLE on every broadcast, so one subagent's new
  // activity string used to hand the sidebar a fresh object for every row. The
  // list reconciles by reference, so all of them were torn down and rebuilt --
  // which closed the tooltip under the pointer and restarted each status dot's
  // pulse.
  it('replace keeps the identity of a row whose fields did not change', () => {
    const store = createBackgroundTaskStore()
    store.replace('a1', [
      proto('t1', BackgroundTaskStatus.RUNNING, 'reading'),
      proto('t2', BackgroundTaskStatus.RUNNING, 'reading'),
    ])
    const [first, second] = store.get('a1')

    store.replace('a1', [
      proto('t1', BackgroundTaskStatus.RUNNING, 'reading'),
      proto('t2', BackgroundTaskStatus.RUNNING, 'writing'),
    ])

    expect(store.get('a1')[0]).toBe(first)
    expect(store.get('a1')[1]).toBe(second)
    expect(store.get('a1')[1]!.activity).toBe('writing')
  })

  it('replace still adds, drops and reorders rows', () => {
    const store = createBackgroundTaskStore()
    store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING), proto('t2', BackgroundTaskStatus.RUNNING)])

    store.replace('a1', [proto('t2', BackgroundTaskStatus.COMPLETED), proto('t3', BackgroundTaskStatus.PENDING)])

    expect(store.get('a1').map(t => t.rowKey)).toEqual(['t2', 't3'])
    expect(store.get('a1').map(t => t.status)).toEqual(['completed', 'pending'])
  })

  // A reconcile writes THROUGH the store proxy, and an agent with no rows holds
  // the one empty array every other agent reads as its default.
  it('replace into a cleared agent leaves every other agent empty', () => {
    const store = createBackgroundTaskStore()
    store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING)])
    store.clear('a1')

    store.replace('a1', [proto('t2', BackgroundTaskStatus.RUNNING)])

    expect(store.get('a1').map(t => t.rowKey)).toEqual(['t2'])
    expect(store.get('a2')).toEqual([])
  })

  it('replace skips an identical re-broadcast', () => {
    const store = createBackgroundTaskStore()
    store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING)])
    const first = store.get('a1')
    store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING)])
    expect(store.get('a1')).toBe(first)
  })

  it('replace applies when activity changed', () => {
    const store = createBackgroundTaskStore()
    store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING, 'a')])
    store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING, 'b')])
    expect(store.get('a1')[0].activity).toBe('b')
  })

  it('remove drops only the targeted agent', () => {
    const store = createBackgroundTaskStore()
    store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING)])
    store.replace('a2', [proto('t2', BackgroundTaskStatus.PENDING)])
    store.remove('a1')
    expect(store.get('a1')).toEqual([])
    expect(store.get('a2')).toHaveLength(1)
  })

  it('a first set (undefined prior) always goes through', () => {
    const store = createBackgroundTaskStore()
    store.replace('a1', [])
    expect(store.get('a1')).toEqual([])
  })

  /**
   * A registry that could not be LOADED is not an empty registry.
   *
   * The section is hidden when it is empty, so the two rendering identically is
   * what let a worker-side failure take the whole section off screen with
   * nothing to say why -- a database missing a column did exactly that, and the
   * only trace was a slog.Warn nobody was reading.
   */
  describe('load failure', () => {
    it('reports no failure until one is recorded', () => {
      const store = createBackgroundTaskStore()
      expect(store.loadFailed('a1')).toBe(false)
      store.replace('a1', [])
      expect(store.loadFailed('a1'), 'an empty registry is not a failure').toBe(false)
    })

    it('records a failure per agent', () => {
      const store = createBackgroundTaskStore()
      store.markLoadFailed('a1')
      expect(store.loadFailed('a1')).toBe(true)
      expect(store.loadFailed('a2'), 'one agent\'s failure is not another\'s').toBe(false)
    })

    // The retry that succeeds is the one that clears it, and an EMPTY successful
    // answer still counts: it says the registry is readable again. Clearing only
    // on a non-empty answer would leave the error on screen for an agent whose
    // tasks were all reclaimed.
    it('clears the failure on the next successful load, empty or not', () => {
      const store = createBackgroundTaskStore()
      store.markLoadFailed('a1')
      store.replace('a1', [])
      expect(store.loadFailed('a1')).toBe(false)

      store.markLoadFailed('a2')
      store.replace('a2', [proto('t1', BackgroundTaskStatus.RUNNING)])
      expect(store.loadFailed('a2')).toBe(false)
    })

    // The identical-rebroadcast short-circuit returns BEFORE the row write, so
    // the flag has to be cleared ahead of it or a recovery whose rows happen to
    // match the last good ones never clears the error.
    it('clears the failure even when the rows are unchanged', () => {
      const store = createBackgroundTaskStore()
      store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING)])
      store.markLoadFailed('a1')
      store.replace('a1', [proto('t1', BackgroundTaskStatus.RUNNING)])
      expect(store.loadFailed('a1')).toBe(false)
    })

    it('drops the failure with the agent', () => {
      const store = createBackgroundTaskStore()
      store.markLoadFailed('a1')
      store.remove('a1')
      expect(store.loadFailed('a1')).toBe(false)

      store.markLoadFailed('a2')
      store.clear('a2')
      expect(store.loadFailed('a2')).toBe(false)
    })
  })
})
