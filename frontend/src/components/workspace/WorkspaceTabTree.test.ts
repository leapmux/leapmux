import { describe, expect, it } from 'vitest'
import { parseSidebarTabDragId, SIDEBAR_TAB_PREFIX } from '~/components/shell/TabDragContext'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'

describe('tabLeaf draggable ID format', () => {
  // TabLeaf builds the id; TabDragContext reads it back. This drives the REAL
  // parser, not a copy of it: a test that round-trips a local encoder against a
  // local decoder proves only that the two agree with each other, and stays
  // green while production drifts away from both.
  function encodeDraggableId(workspaceId: string, tabType: TabType, tabId: string): string {
    return `${SIDEBAR_TAB_PREFIX}${workspaceId}:${tabType}:${tabId}`
  }

  it('roundtrips agent tab ID', () => {
    const parsed = parseSidebarTabDragId(encodeDraggableId('ws-abc', TabType.AGENT, 'agent-123'))
    expect(parsed).not.toBeNull()
    expect(parsed!.workspaceId).toBe('ws-abc')
    expect(parsed!.tabKey).toBe(`${TabType.AGENT}:agent-123`)
  })

  it('roundtrips terminal tab ID', () => {
    const parsed = parseSidebarTabDragId(encodeDraggableId('ws-xyz', TabType.TERMINAL, 'term-456'))
    expect(parsed).not.toBeNull()
    expect(parsed!.workspaceId).toBe('ws-xyz')
    expect(parsed!.tabKey).toBe(`${TabType.TERMINAL}:term-456`)
  })

  it('handles workspace ID with hyphens and UUIDs', () => {
    const wsId = '550e8400-e29b-41d4-a716-446655440000'
    const parsed = parseSidebarTabDragId(encodeDraggableId(wsId, TabType.AGENT, 'a1'))
    expect(parsed!.workspaceId).toBe(wsId)
  })

  it('splits at the FIRST colon, so an id holding one stays whole', () => {
    // The tabKey is itself `${type}:${id}`, and an agent id may carry more
    // colons. Splitting anywhere else routes the drop at the wrong tab.
    const parsed = parseSidebarTabDragId(encodeDraggableId('ws-1', TabType.AGENT, 'a:b:c'))
    expect(parsed!.workspaceId).toBe('ws-1')
    expect(parsed!.tabKey).toBe(`${TabType.AGENT}:a:b:c`)
  })

  it('returns null for non-sidebar-tab IDs', () => {
    expect(parseSidebarTabDragId('1:agent-1')).toBeNull()
    expect(parseSidebarTabDragId('ws-drop:ws-1')).toBeNull()
    expect(parseSidebarTabDragId('')).toBeNull()
  })

  it('returns null for malformed sidebar-tab ID without colon', () => {
    expect(parseSidebarTabDragId(`${SIDEBAR_TAB_PREFIX}nocolon`)).toBeNull()
  })
})
