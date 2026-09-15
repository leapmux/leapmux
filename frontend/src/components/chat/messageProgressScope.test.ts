import type { MessageContextResolver } from './messageContextResolver'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { describe, expect, it } from 'vitest'
import { parseMessageContent } from '~/lib/messageParser'
import { createToolProgressStore } from '~/stores/chatToolProgress'
import { testMessageContext } from '~/test-support/messageContext'
import { makeMessage } from '~/test-support/messageFactory'
import { createMessageRenderSources } from './messageContextResolver'

/** The provider session every span below belongs to, unless a test states another. */
const SESSION = 'sess-1'

function renderSources(context: MessageContextResolver, message: AgentChatMessage) {
  return createMessageRenderSources(() => context, () => message, () => parseMessageContent(message))
}

function runningToolContext(spanId: string, elapsedSeconds: number, agentSessionId: string = SESSION) {
  const progress = createToolProgressStore()
  progress.apply('agent', { spanId, agentSessionId, elapsedSeconds })
  return { progress, context: testMessageContext({ progress: identity => progress.get('agent', identity) }) }
}

describe('message progress scope', () => {
  it('attaches progress to the row of the session the update states', () => {
    // The worker states the session on the payload, so a child transcript whose
    // rows carry the ROOT session id reaches its own entry: the update that runs
    // that tool carries the same root id.
    const { context } = runningToolContext('running-tool', 30, 'root-session')
    const child = makeMessage({ id: 'child', spanId: 'running-tool', agentSessionId: 'root-session' })
    expect(renderSources(context, child).progress()?.elapsedSeconds).toBe(30)
  })

  it('reports no progress for a row of another provider session', () => {
    // The span key makes a cross-session read impossible: one provider gives a
    // span id that is unique inside a session alone, so the same id in another
    // session addresses a different tool.
    const { context } = runningToolContext('running-tool', 30, 'root-session')
    const other = makeMessage({ id: 'other', spanId: 'running-tool', agentSessionId: 'later-session' })
    const unstamped = makeMessage({ id: 'unstamped', spanId: 'running-tool', agentSessionId: '' })
    expect(renderSources(context, other).progress()).toBeUndefined()
    expect(renderSources(context, unstamped).progress()).toBeUndefined()
  })

  it('reports each later update of the same span', () => {
    const { progress, context } = runningToolContext('running-tool', 30)
    const sources = renderSources(context, makeMessage({ spanId: 'running-tool', agentSessionId: SESSION }))
    expect(sources.progress()?.elapsedSeconds).toBe(30)
    progress.apply('agent', { spanId: 'running-tool', agentSessionId: SESSION, elapsedSeconds: 0 })
    expect(sources.progress()?.elapsedSeconds).toBe(0)
  })

  it('reports no progress for a row that has no span', () => {
    const { context } = runningToolContext('running-tool', 30)
    expect(renderSources(context, makeMessage({ spanId: '', agentSessionId: SESSION })).progress()).toBeUndefined()
  })

  it('reports no progress for a different span', () => {
    const { context } = runningToolContext('running-tool', 30)
    expect(renderSources(context, makeMessage({ spanId: 'other-tool', agentSessionId: SESSION })).progress()).toBeUndefined()
  })

  it('drops the progress of one span when its tool finishes', () => {
    const { progress, context } = runningToolContext('running-tool', 30)
    const sources = renderSources(context, makeMessage({ spanId: 'running-tool', agentSessionId: SESSION }))
    expect(sources.progress()?.elapsedSeconds).toBe(30)
    progress.drop('agent', { spanId: 'running-tool', agentSessionId: SESSION })
    expect(sources.progress()).toBeUndefined()
  })

  it('drops the progress of every span when a turn boundary clears the agent', () => {
    // The two clears limit an entry's life: the result row drops one span, and a
    // lifecycle event clears the agent. They stay the only removals, because no
    // provider reports the end of a running tool.
    const { progress, context } = runningToolContext('running-tool', 30)
    const sources = renderSources(context, makeMessage({ spanId: 'running-tool', agentSessionId: SESSION }))
    expect(sources.progress()?.elapsedSeconds).toBe(30)
    progress.clearAgent('agent')
    expect(sources.progress()).toBeUndefined()
  })
})
