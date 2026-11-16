import { render, waitFor } from '@solidjs/testing-library'
import { batch, createMemo } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { createToolProgressStore } from '~/stores/chatToolProgress'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { renderListedMessageContent } from '~/test-support/messageContentRenderer'
import { createListedRequestRows, createMutableTranscript } from '~/test-support/messageContext'
import { makeMessage, makeTranscriptMessage, rawContent } from '~/test-support/messageFactory'
import { createTranscriptScenario } from '~/test-support/transcriptScenario'
import { createMessageContextResolver, createMessageRenderSources } from '../../messageContextResolver'
import { classifyLettaMessage } from './classification'
import './plugin'
import '~/components/chat/providers'

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
      tool_returns: [
        { tool_call_id: 'native-progress-output', status: 'success', tool_return: 'NATIVEFIRST\ncurrent native output' },
      ],
    }
    const fields = {
      agentProvider: AgentProvider.LETTA,
      spanId: 'letta-tool-native-progress-output',
      agentSessionId: 'local-conv-4',
      spanType: 'Bash',
    }
    const opening = makeMessage({
      ...fields,
      id: 'native-opener',
      seq: 1n,
      content: rawContent(request),
    })
    const window = makeMessage({
      ...fields,
      id: 'native-window',
      seq: 2n,
      content: rawContent(progress),
    })
    const { container } = render(() => {
      const live = createToolProgressStore()
      live.apply('test', { spanId: fields.spanId, agentSessionId: fields.agentSessionId, outputTail: 'current native output' })
      const transcript = createMutableTranscript([opening, window], {
        progress: identity => live.get('test', identity),
      })
      const resolver = createMessageContextResolver(transcript.sources)
      const resolved = resolver.resolvedMessage(opening).resolved
      const sources = createMessageRenderSources(() => resolver, () => opening, () => resolved)
      const visibleRows = createListedRequestRows(resolver, transcript.sources.messages)
      expect(visibleRows({ spanId: fields.spanId, agentSessionId: fields.agentSessionId })).toEqual({ request: true })
      return renderListedMessageContent(request, {
        premeasureMode: true,
        sources,
        toolProgress: {
          liveTail: () => sources.progress(),
        },
      }, classifyLettaMessage({ ...resolved, agentProvider: AgentProvider.LETTA }), AgentProvider.LETTA)
    })
    expect(container.textContent).toContain('current native output')
  })

  it('replaces the live window, hides an empty end, and draws the genuine final once', () => {
    const fields = {
      agentProvider: AgentProvider.LETTA,
      spanId: 'letta-tool-native-progress-output',
      agentSessionId: 'local-conv-3',
      spanType: 'Bash',
    }
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
      tool_returns: [
        { tool_call_id: 'native-progress-output', status: 'success', tool_return: text },
      ],
    })
    const opening = makeMessage({
      ...fields,
      id: 'letta-msg-1',
      seq: 1n,
      content: rawContent(request),
    })
    const progress = makeMessage({
      ...fields,
      id: 'synthetic-tool-return-stream-native-progress-output',
      seq: 2n,
      content: rawContent(currentWindow('first native window')),
    })
    let state: {
      live: ReturnType<typeof createToolProgressStore>
      transcript: ReturnType<typeof createMutableTranscript>
      resolver: ReturnType<typeof createMessageContextResolver>
      visibleRows: ReturnType<typeof createListedRequestRows>
    } | undefined
    const { container } = render(() => {
      const live = createToolProgressStore()
      live.apply('test', { ...fields, outputTail: 'first native window' })
      const transcript = createMutableTranscript([opening, progress], {
        progress: identity => live.get('test', identity),
      })
      const resolver = createMessageContextResolver(transcript.sources)
      state = {
        live,
        transcript,
        resolver,
        visibleRows: createListedRequestRows(resolver, transcript.sources.messages),
      }
      const rows = createMemo(() => transcript.sources.messages().map((message) => {
        const resolved = resolver.resolvedMessage(message).resolved
        const sources = createMessageRenderSources(() => resolver, () => message, () => resolved)
        return renderListedMessageContent(resolved.parentObject, {
          premeasureMode: true,
          spanType: message.spanType,
          sources,
          toolProgress: {
            liveTail: () => sources.progress(),
          },
        }, classifyLettaMessage({ ...resolved, agentProvider: AgentProvider.LETTA }), AgentProvider.LETTA, message.completion)
      }))
      return <>{rows()}</>
    })
    if (!state)
      throw new Error('The native tool renderer did not create its transcript.')
    expect(container.textContent).toContain('first native window')
    expect(state.visibleRows(fields)).toEqual({ request: true })

    const second = currentWindow('second native window')
    const { live, transcript, resolver, visibleRows } = state
    batch(() => {
      transcript.broadcast(makeMessage({
        ...fields,
        id: progress.id,
        seq: progress.seq,
        content: rawContent(second),
      }))
      live.apply('test', { ...fields, outputTail: 'second native window' })
    })
    expect(container.textContent).toContain('second native window')
    expect(container.textContent).not.toContain('first native window')
    expect(resolver.result(fields)).toBeUndefined()

    const end = {
      id: 'letta-msg-1',
      message_type: 'client_tool_end',
      run_id: 'local-run-1',
      tool_call_id: 'native-progress-output',
      status: 'success',
    }
    const endMessage = makeMessage({
      ...fields,
      id: 'native-empty-end',
      seq: 3n,
      content: rawContent(end),
    })
    batch(() => {
      transcript.append(endMessage)
      live.drop('test', fields)
    })
    expect(container.textContent).not.toContain('second native window')
    expect(visibleRows(fields)).toEqual({ request: true })
    expect(resolver.result(fields)).toBeUndefined()

    const final = {
      type: 'message',
      id: 'synthetic-tool-return-native-final',
      message_type: 'tool_return_message',
      run_id: 'local-run-1',
      tool_call_id: 'native-progress-output',
      status: 'success',
      tool_return: 'Complete native result.',
    }
    const finalMessage = makeMessage({
      ...fields,
      id: final.id,
      seq: 4n,
      content: rawContent(final),
    })
    transcript.append(finalMessage)
    expect(visibleRows(fields)).toEqual({ request: true })
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
  const listStart = {
    id: 'letta-msg-9',
    message_type: 'client_tool_start',
    tool_call_id: 'list-tasks',
    run_id: 'local-run-5',
    tool_name: 'TaskList',
    tool_args: '{}',
  }
  const listReturn = {
    id: 'synthetic-tool-return-list',
    message_type: 'tool_return_message',
    tool_call_id: 'list-tasks',
    run_id: 'local-run-5',
    status: 'success',
    tool_return: JSON.stringify({
      tasks: [
        {
          taskId: 'task_1',
          subject: 'Inspect the repository',
          description: 'Read the repository files.',
          status: 'completed',
          blocks: [],
          blockedBy: [],
          metadata: {},
        },
        {
          taskId: 'task_2',
          subject: 'List three checks',
          description: 'List three checks to run.',
          status: 'in_progress',
          blocks: [],
          blockedBy: [],
          metadata: {},
        },
      ],
    }),
  }

  it('draws a running TaskCreate before its answer lands', async () => {
    const scenario = createTranscriptScenario({
      archive: [
        makeTranscriptMessage({
          id: 'create-request',
          provider: AgentProvider.LETTA,
          spanId: 'letta-tool-create-first',
          spanType: 'TaskCreate',
          agentSessionId: SESSION,
          content: createStart,
        }, 1n),
      ],
    })
    // The list draws the carried task on the pending row below the request row.
    const { container } = scenario.renderRows('create-request')
    await waitFor(() => expect(container.querySelector('[data-testid="tool-pending"]')?.textContent).toContain('Inspect the repository'))
    await settleWorkerReplies()
    expect(container.textContent).toContain('Read the repository files.')
  })

  it('draws the checklist that a native TaskList answer returns', async () => {
    const scenario = createTranscriptScenario({
      archive: [
        makeTranscriptMessage({
          id: 'list-request',
          provider: AgentProvider.LETTA,
          spanId: 'letta-tool-list-tasks',
          spanType: 'TaskList',
          agentSessionId: SESSION,
          content: listStart,
        }, 1n),
        makeTranscriptMessage({
          id: 'list-result',
          provider: AgentProvider.LETTA,
          spanId: 'letta-tool-list-tasks',
          agentSessionId: SESSION,
          content: listReturn,
        }, 2n),
      ],
    })
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
    tool_returns: [
      { tool_call_id: 'ask-1', status: 'success', tool_return: RECEIPT },
    ],
  }
  const archive = [
    makeTranscriptMessage({
      id: 'ask-request',
      provider: AgentProvider.LETTA,
      spanId: 'letta-tool-ask-1',
      spanType: 'AskUserQuestion',
      agentSessionId: SESSION,
      content: start,
    }, 1n),
    makeTranscriptMessage({
      id: 'ask-result',
      provider: AgentProvider.LETTA,
      spanId: 'letta-tool-ask-1',
      agentSessionId: SESSION,
      content: receipt,
    }, 2n),
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

describe('letta tool call that its turn outlived', () => {
  // Letta Code sends no answer for such a call. The Worker stores the start frame of
  // the call again as its closing row, with the completion that states how the call
  // ended.
  const SESSION = 'local-conv-1'
  const command = 'node held-native-script.js'
  const start = {
    id: 'letta-msg-1',
    message_type: 'client_tool_start',
    tool_call_id: 'outlived-call',
    run_id: 'local-run-1',
    tool_name: 'Bash',
    tool_args: JSON.stringify({ command, description: 'Run the held command' }),
  }
  const span = {
    provider: AgentProvider.LETTA,
    spanId: 'letta-tool-outlived-call',
    spanType: 'Bash',
    agentSessionId: SESSION,
  }

  function outlivedCall(completion: MessageCompletion, windowIds?: readonly string[]) {
    return createTranscriptScenario({
      archive: [
        makeTranscriptMessage({ ...span, id: 'outlived-request', content: start }, 1n),
        makeTranscriptMessage({
          ...span,
          id: 'outlived-closing',
          content: start,
          completion,
        }, 2n),
      ],
      ...(windowIds !== undefined
        ? { windowIds }
        : {}),
    })
  }

  it.each([
    ['INTERRUPTED', MessageCompletion.INTERRUPTED, 'cancelled'],
    ['ERROR', MessageCompletion.ERROR, 'failed'],
    // `client_tool_end` stated success, but no answer followed: the call finished and
    // holds no result.
    ['COMPLETE', MessageCompletion.COMPLETE, 'incomplete'],
  ] as const)('ends the call with its closing row: %s', (_name, completion, status) => {
    const scenario = outlivedCall(completion)
    expect(scenario.entry('outlived-closing').category).toEqual({ kind: 'tool_result' })
    expect(scenario.resolver.result(span)?.message.id).toBe('outlived-closing')
    expect(scenario.resolver.request(span)?.message.id).toBe('outlived-request')
    const row = scenario.toolRow('outlived-closing')
    expect(row.call.status).toBe(status)
    expect(row.call.request).toMatchObject({ command })
    // Letta Code sent no output, so the row claims none.
    expect(row.call.result).toBeUndefined()
  })

  it('opens no pending row below the request of a call that its closing row ended', () => {
    const { container } = outlivedCall(MessageCompletion.INTERRUPTED).renderRows('outlived-request')
    expect(container.querySelector('[data-testid="tool-pending"]')).toBeNull()
    expect(container.textContent).toContain(command)
  })

  // A window that starts after the request holds the closing row alone. That row then
  // draws the call that it ends.
  it('draws the command on a closing row whose request is outside the window', () => {
    const { container } = outlivedCall(MessageCompletion.INTERRUPTED, ['outlived-closing']).renderBubble('outlived-closing')
    expect(container.textContent).toContain(command)
  })

  // A file change reads its own arguments as the changes it asked for. Only a call that
  // finished keeps them on its closing row; a stopped or failed one changed nothing that
  // the row can state.
  it.each([
    [
      'INTERRUPTED',
      MessageCompletion.INTERRUPTED,
      'cancelled',
      false,
    ],
    [
      'ERROR',
      MessageCompletion.ERROR,
      'failed',
      false,
    ],
    [
      'COMPLETE',
      MessageCompletion.COMPLETE,
      'incomplete',
      true,
    ],
  ] as const)('ends a file change with its closing row: %s', (_name, completion, status, drawsChanges) => {
    const edit = {
      ...start,
      tool_call_id: 'outlived-edit',
      tool_name: 'Edit',
      tool_args: JSON.stringify({ file_path: '/src/main.ts', old_string: 'OLDTEXT', new_string: 'NEWTEXT' }),
    }
    const editSpan = { ...span, spanId: 'letta-tool-outlived-edit', spanType: 'Edit' }
    const scenario = createTranscriptScenario({
      archive: [
        makeTranscriptMessage({ ...editSpan, id: 'edit-request', content: edit }, 1n),
        makeTranscriptMessage({
          ...editSpan,
          id: 'edit-closing',
          content: edit,
          completion,
        }, 2n),
      ],
      windowIds: ['edit-closing'],
    })
    const row = scenario.toolRow('edit-closing')
    expect(row.call).toMatchObject({
      kind: 'edit',
      status,
      request: {
        changes: [
          { filePath: '/src/main.ts', oldStr: 'OLDTEXT', newStr: 'NEWTEXT' },
        ],
      },
    })
    expect(row.call.result).toBeUndefined()
    const { container } = scenario.renderBubble('edit-closing')
    expect(container.textContent).toContain('main.ts')
    if (drawsChanges)
      expect(container.textContent).toContain('NEWTEXT')
    else
      expect(container.textContent).not.toContain('NEWTEXT')
  })

  // A native message can open the call instead of `client_tool_start`.
  it('ends a call that a native call message opened', () => {
    const call = {
      type: 'message',
      message_type: 'tool_call_message',
      run_id: 'local-run-1',
      tool_calls: [
        {
          tool_call_id: 'outlived-read',
          name: 'Read',
          arguments: JSON.stringify({ file_path: '/src/notes.txt' }),
        },
      ],
    }
    const readSpan = { ...span, spanId: 'letta-tool-outlived-read', spanType: 'Read' }
    const scenario = createTranscriptScenario({
      archive: [
        makeTranscriptMessage({ ...readSpan, id: 'read-request', content: call }, 1n),
        makeTranscriptMessage({
          ...readSpan,
          id: 'read-closing',
          content: call,
          completion: MessageCompletion.INTERRUPTED,
        }, 2n),
      ],
    })
    expect(scenario.entry('read-closing').category).toEqual({ kind: 'tool_result' })
    expect(scenario.resolver.result(readSpan)?.message.id).toBe('read-closing')
    const row = scenario.toolRow('read-closing')
    expect(row.call.status).toBe('cancelled')
    expect(row.call.request).toMatchObject({ path: '/src/notes.txt' })
    expect(row.call.result).toBeUndefined()
  })
})
