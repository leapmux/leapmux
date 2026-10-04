import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { BackgroundTaskKind, ListAgentMessagesResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { requireNativeSidebarSnapshot } from './nativeSidebarSnapshot'

describe('requireNativeSidebarSnapshot', () => {
  it('keeps an authoritative empty snapshot', () => {
    const response = create(ListAgentMessagesResponseSchema, { todosLoaded: true, backgroundTasksLoaded: true })
    expect(requireNativeSidebarSnapshot(response)).toBe(response)
  })

  it('keeps actual task identity and group fields', () => {
    const response = create(ListAgentMessagesResponseSchema, {
      todosLoaded: true,
      backgroundTasksLoaded: true,
      backgroundTasks: [{ id: 'task-1', kind: BackgroundTaskKind.SUBAGENT, childAgentId: 'child-1', groupKey: 'workflow-1', groupLabel: 'Native workflow' }],
    })
    expect(requireNativeSidebarSnapshot(response).backgroundTasks[0]).toMatchObject({ id: 'task-1', childAgentId: 'child-1', groupKey: 'workflow-1', groupLabel: 'Native workflow' })
  })

  it.each([
    { todosLoaded: false, backgroundTasksLoaded: false },
    { todosLoaded: true, backgroundTasksLoaded: false },
    { todosLoaded: false, backgroundTasksLoaded: true },
    {},
  ])('refuses incomplete loaded flags: %j', (fields) => {
    const response = create(ListAgentMessagesResponseSchema, fields)
    expect(() => requireNativeSidebarSnapshot(response)).toThrow('The Worker did not load the native task and to-do snapshots.')
  })
})
