import { render, waitFor } from '@solidjs/testing-library'
import { batch, createMemo } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createToolProgressStore } from '~/stores/chatToolProgress'
import { createMutableTranscript } from '~/test-support/messageContext'
import { makeMessage, makeTranscriptMessage, rawContent } from '~/test-support/messageFactory'
import { createTranscriptScenario } from '~/test-support/transcriptScenario'
import { renderMessageContent } from '../../messageContentRenderer'
import { createMessageContextResolver, createMessageRenderSources } from '../../messageContextResolver'
import { classifyLettaMessage } from './classification'
import './plugin'
import '../testMocks'

describe('letta live tool output', () => {
  it('draws the current native window beside a request with hidden progress records', () => {
    const request = {
      id: 'letta-msg-1',
      message_type: 'client_tool_start',
      tool_call_id: 'native-progress-output',
      run_id: 'local-run-1',
      tool_name: 'Bash',
      tool_args: '{"command":"node actual-native-script.js","description":"Run the scripted command"}',
    }
    const progress = {
      type: 'message',
      message_type: 'tool_return_message',
      id: 'synthetic-tool-return-stream-native-progress-output',
      tool_call_id: 'native-progress-output',
      run_id: 'local-run-1',
      status: 'success',
      tool_return: 'NATIVEFIRST\ncurrent native output',
      tool_returns: [{ tool_call_id: 'native-progress-output', status: 'success', tool_return: 'NATIVEFIRST\ncurrent native output' }],
    }
    const fields = { agentProvider: AgentProvider.LETTA, spanId: 'letta-tool-native-progress-output', agentSessionId: 'local-conv-4', spanType: 'Bash' }
    const opening = makeMessage({ ...fields, id: 'native-opener', seq: 1n, content: rawContent(request) })
    const window = makeMessage({ ...fields, id: 'native-window', seq: 2n, content: rawContent(progress) })
    const { container } = render(() => {
      const live = createToolProgressStore()
      live.apply('test', { spanId: fields.spanId, agentSessionId: fields.agentSessionId, outputTail: 'current native output' })
      const transcript = createMutableTranscript([opening, window], { progress: identity => live.get('test', identity) })
      const resolver = createMessageContextResolver(transcript.sources)
      const resolved = resolver.resolvedMessage(opening).resolved
      const sources = createMessageRenderSources(() => resolver, () => opening, () => resolved)
      expect(resolver.visibleRows({ spanId: fields.spanId, agentSessionId: fields.agentSessionId })).toEqual({ request: true, result: false })
      return renderMessageContent(request, {
        premeasureMode: true,
        sources,
        toolProgress: { liveTail: () => sources.progress() },
      }, classifyLettaMessage({ ...resolved, agentProvider: AgentProvider.LETTA }), AgentProvider.LETTA)
    })
    expect(container.textContent).toContain('current native output')
  })

  it('replaces the live window, hides an empty end, and draws the genuine final once', () => {
    const fields = { agentProvider: AgentProvider.LETTA, spanId: 'letta-tool-native-progress-output', agentSessionId: 'local-conv-3', spanType: 'Bash' }
    const request = {
      id: 'letta-msg-1',
      message_type: 'client_tool_start',
      tool_call_id: 'native-progress-output',
      run_id: 'local-run-1',
      tool_name: 'Bash',
      tool_args: '{"command":"node actual-native-script.js","description":"Run the scripted command"}',
    }
    const currentWindow = (text: string) => ({
      type: 'message',
      message_type: 'tool_return_message',
      id: 'synthetic-tool-return-stream-native-progress-output',
      tool_call_id: 'native-progress-output',
      run_id: 'local-run-1',
      status: 'success',
      tool_return: text,
      tool_returns: [{ tool_call_id: 'native-progress-output', status: 'success', tool_return: text }],
    })
    const opening = makeMessage({ ...fields, id: 'letta-msg-1', seq: 1n, content: rawContent(request) })
    const progress = makeMessage({ ...fields, id: 'synthetic-tool-return-stream-native-progress-output', seq: 2n, content: rawContent(currentWindow('first native window')) })
    let state: {
      live: ReturnType<typeof createToolProgressStore>
      transcript: ReturnType<typeof createMutableTranscript>
      resolver: ReturnType<typeof createMessageContextResolver>
    } | undefined
    const { container } = render(() => {
      const live = createToolProgressStore()
      live.apply('test', { ...fields, outputTail: 'first native window' })
      const transcript = createMutableTranscript([opening, progress], { progress: identity => live.get('test', identity) })
      const resolver = createMessageContextResolver(transcript.sources)
      state = { live, transcript, resolver }
      const rows = createMemo(() => transcript.sources.messages().map((message) => {
        const resolved = resolver.resolvedMessage(message).resolved
        const sources = createMessageRenderSources(() => resolver, () => message, () => resolved)
        return renderMessageContent(resolved.parentObject, {
          premeasureMode: true,
          spanType: message.spanType,
          sources,
          toolProgress: { liveTail: () => sources.progress() },
        }, classifyLettaMessage({ ...resolved, agentProvider: AgentProvider.LETTA }), AgentProvider.LETTA, message.completion)
      }))
      return <>{rows()}</>
    })
    if (!state)
      throw new Error('The native tool renderer did not create its transcript.')
    expect(container.textContent).toContain('first native window')
    expect(state.resolver.visibleRows(fields)).toEqual({ request: true, result: false })

    const second = currentWindow('second native window')
    const { live, transcript, resolver } = state
    batch(() => {
      transcript.broadcast(makeMessage({ ...fields, id: progress.id, seq: progress.seq, content: rawContent(second) }))
      live.apply('test', { ...fields, outputTail: 'second native window' })
    })
    expect(container.textContent).toContain('second native window')
    expect(container.textContent).not.toContain('first native window')
    expect(resolver.result(fields)).toBeUndefined()

    const end = { id: 'letta-msg-1', message_type: 'client_tool_end', run_id: 'local-run-1', tool_call_id: 'native-progress-output', status: 'success' }
    const endMessage = makeMessage({ ...fields, id: 'native-empty-end', seq: 3n, content: rawContent(end) })
    batch(() => {
      transcript.append(endMessage)
      live.drop('test', fields)
    })
    expect(container.textContent).not.toContain('second native window')
    expect(resolver.visibleRows(fields)).toEqual({ request: true, result: false })
    expect(resolver.result(fields)).toBeUndefined()

    const final = { type: 'message', id: 'synthetic-tool-return-native-final', message_type: 'tool_return_message', run_id: 'local-run-1', tool_call_id: 'native-progress-output', status: 'success', tool_return: 'Complete native result.' }
    const finalMessage = makeMessage({ ...fields, id: final.id, seq: 4n, content: rawContent(final) })
    transcript.append(finalMessage)
    expect(resolver.visibleRows(fields)).toEqual({ request: true, result: true })
    expect(resolver.request(fields)?.message.id).toBe(opening.id)
    expect(resolver.result(fields)?.message.id).toBe(finalMessage.id)
    expect(container.textContent?.match(/Complete native result\./g)).toHaveLength(1)
    expect(container.textContent).not.toContain('second native window')
    expect(transcript.sources.messages().map(message => new TextDecoder().decode(message.content))).toEqual([
      JSON.stringify(request),
      JSON.stringify(second),
      JSON.stringify(end),
      JSON.stringify(final),
    ])
  })
})

describe('letta native task rendering', () => {
  // The worker stores each native task frame as its own row on the call's span.
  const SESSION = 'local-conv-1'
  const createStart = {
    id: 'letta-msg-1',
    message_type: 'client_tool_start',
    tool_call_id: 'create-first',
    run_id: 'local-run-1',
    tool_name: 'TaskCreate',
    tool_args: '{"subject":"Inspect the repository","description":"Read the repository files.","activeForm":"Inspecting the repository"}',
  }
  const listStart = { id: 'letta-msg-9', message_type: 'client_tool_start', tool_call_id: 'list-tasks', run_id: 'local-run-5', tool_name: 'TaskList', tool_args: '{}' }
  const listReturn = {
    id: 'synthetic-tool-return-list',
    message_type: 'tool_return_message',
    tool_call_id: 'list-tasks',
    run_id: 'local-run-5',
    status: 'success',
    tool_return: JSON.stringify({ tasks: [
      { taskId: 'task_1', subject: 'Inspect the repository', description: 'Read the repository files.', status: 'completed', blocks: [], blockedBy: [], metadata: {} },
      { taskId: 'task_2', subject: 'List three checks', description: 'List three checks to run.', status: 'in_progress', blocks: [], blockedBy: [], metadata: {} },
    ] }),
  }

  it('draws a running TaskCreate before its answer lands', async () => {
    const scenario = createTranscriptScenario({ archive: [
      makeTranscriptMessage({ id: 'create-request', provider: AgentProvider.LETTA, spanId: 'letta-tool-create-first', spanType: 'TaskCreate', agentSessionId: SESSION, content: createStart }, 1n),
    ] })
    const { container } = scenario.renderBubble('create-request')
    await waitFor(() => expect(container.textContent).toContain('Inspect the repository'))
    expect(container.textContent).toContain('Read the repository files.')
  })

  it('draws the checklist that a native TaskList answer returns', async () => {
    const scenario = createTranscriptScenario({ archive: [
      makeTranscriptMessage({ id: 'list-request', provider: AgentProvider.LETTA, spanId: 'letta-tool-list-tasks', spanType: 'TaskList', agentSessionId: SESSION, content: listStart }, 1n),
      makeTranscriptMessage({ id: 'list-result', provider: AgentProvider.LETTA, spanId: 'letta-tool-list-tasks', agentSessionId: SESSION, content: listReturn }, 2n),
    ] })
    const { container } = scenario.renderBubble('list-result')
    await waitFor(() => expect(container.textContent).toContain('List three checks'))
    expect(container.textContent).toContain('Inspect the repository')
    expect(container.querySelectorAll('[data-task-checkbox="completed"]')).toHaveLength(1)
    expect(container.querySelectorAll('[data-task-checkbox="in_progress"]')).toHaveLength(1)
    expect(container.textContent).not.toContain('"taskId"')
  })
})

describe('letta question receipt rendering', () => {
  // Letta Code 0.34 returns a receipt at once for an AskUserQuestion call. The frames are
  // verbatim from a live 0.34.2 run (probe `question`).
  const SESSION = 'local-conv-1'
  const RECEIPT = '{"type":"ask_user_question","version":2,"toolCallId":"ask-1","questions":[{"question":"Which color do you prefer?","header":"Color","options":[{"label":"Blue","description":"The color blue"},{"label":"Red","description":"The color red"}]}],"message":"Questions posted. Answers or dismissal will arrive later in a task notification. You may continue working; do not assume an answer."}'
  const start = {
    id: 'letta-msg-1',
    message_type: 'client_tool_start',
    run_id: 'local-run-1',
    tool_call_id: 'ask-1',
    tool_name: 'AskUserQuestion',
    tool_args: '{"questions":[{"question":"Which color do you prefer?","header":"Color","options":[{"label":"Blue","description":"The color blue"},{"label":"Red","description":"The color red"}]}]}',
  }
  const receipt = {
    type: 'message',
    id: 'synthetic-tool-return-106c4f93-c4ce-40b8-a64d-c7acf4988a83',
    message_type: 'tool_return_message',
    run_id: 'local-run-1',
    status: 'success',
    tool_call_id: 'ask-1',
    tool_return: RECEIPT,
    tool_returns: [{ tool_call_id: 'ask-1', status: 'success', tool_return: RECEIPT }],
  }
  const archive = [
    makeTranscriptMessage({ id: 'ask-request', provider: AgentProvider.LETTA, spanId: 'letta-tool-ask-1', spanType: 'AskUserQuestion', agentSessionId: SESSION, content: start }, 1n),
    makeTranscriptMessage({ id: 'ask-result', provider: AgentProvider.LETTA, spanId: 'letta-tool-ask-1', agentSessionId: SESSION, content: receipt }, 2n),
  ]

  it('draws the questions and not the receipt on the request row', async () => {
    const { container } = createTranscriptScenario({ archive }).renderBubble('ask-request')
    await waitFor(() => expect(container.textContent).toContain('Which color do you prefer?'))
    expect(container.textContent).not.toContain('ask_user_question')
    expect(container.textContent).not.toContain('toolCallId')
  })

  it('draws the message of the receipt and not its JSON on the result row', async () => {
    const { container } = createTranscriptScenario({ archive }).renderBubble('ask-result')
    await waitFor(() => expect(container.textContent).toContain('Questions posted. Answers or dismissal will arrive later in a task notification.'))
    expect(container.textContent).not.toContain('ask_user_question')
    expect(container.textContent).not.toContain('toolCallId')
  })
})
