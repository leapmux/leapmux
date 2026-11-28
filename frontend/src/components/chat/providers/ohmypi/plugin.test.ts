import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerRow, providerToolCall } from '~/test-support/toolCallFixture'
import { providerFor } from '../registry'
import { classifyOhMyPiMessage } from './classification'
import { ohMyPiCompactionBoundary, ohMyPiCompactionStart, ohMyPiNotificationEntry } from './extractors/notification'
import { ohMyPiResultDivider } from './extractors/resultDivider'
import { ohMyPiExtractRow } from './extractors/row'
import { ohMyPiPermissionPresets } from './permissionPresets'
import { ohMyPiControls } from './pluginControls'
import { resolveOhMyPiMessage } from './resolveMessage'
import { ohMyPiValidateResumeHandle } from './resumeHandle'
import { ohMyPiContextUsageFromMessage } from './sessionMetadata'
import { ohMyPiNeedsRequest, ohMyPiSpanRole } from './spanRole'
import './plugin'

const plugin = providerFor(AgentProvider.OH_MY_PI)!
const controls = plugin.controls!

describe('ohmypi plugin', () => {
  // The test compares each hook by identity: a plugin that registers another
  // provider's reader for one hook is still "defined", and only the identity finds it.
  it('is registered for the OH_MY_PI provider with the omp reader for each hook', () => {
    expect(plugin.transcript).toEqual({
      resolveMessage: resolveOhMyPiMessage,
      spanRole: ohMyPiSpanRole,
      needsRequest: ohMyPiNeedsRequest,
      classify: classifyOhMyPiMessage,
      extractRow: ohMyPiExtractRow,
      notificationEntry: ohMyPiNotificationEntry,
      compactionStart: ohMyPiCompactionStart,
      extractDivider: ohMyPiResultDivider,
    })
    expect(plugin.controls).toBe(ohMyPiControls)
    expect(plugin.controls).toEqual({
      permissionPresets: ohMyPiPermissionPresets,
    })
    expect(plugin.session?.contextUsageFromMessage).toBe(ohMyPiContextUsageFromMessage)
    expect(plugin.session?.compactionBoundaryFromMessage).toBe(ohMyPiCompactionBoundary)
    expect(plugin.session?.validateResumeHandle).toBe(ohMyPiValidateResumeHandle)
  })

  // The worker's ValidateAttachment refuses a PDF and a binary file with the same policy.
  it('accepts text and images, and no PDF or binary', () => {
    expect(plugin.configuration?.attachments).toEqual({
      text: true,
      image: true,
      pdf: false,
      audio: false,
      binary: false,
    })
  })

  it('labels the settings trigger with the approval mode', () => {
    expect(plugin.configuration?.triggerModeGroupKey).toBe('permissionMode')
  })

  it('offers the Bypass preset as yolo and no Smart preset', () => {
    expect(controls.permissionPresets).toEqual({
      bypass: {
        sets: { permissionMode: 'yolo' },
      },
    })
  })

  it('states the session as a file', () => {
    expect(plugin.session?.sessionIdIsFilePath).toBe(true)
    expect(plugin.session?.validateResumeHandle?.('01a0cf77')).toBeNull()
  })
})

describe('ohmypi rows', () => {
  it('reads an assistant message as its text, and leaves its thinking to the row the worker writes for it', () => {
    // omp 18.2.11's own frame (probe s1), shortened. The worker persists the thinking
    // as a reasoning row before this one, which the shared transcript draws.
    const text = providerRow(AgentProvider.OH_MY_PI, {
      type: 'message_end',
      message: {
        role: 'assistant',
        content: [
          { type: 'thinking', thinking: 'Greet.' },
          { type: 'text', text: 'Hello from the mock model.' },
        ],
        stopReason: 'stop',
      },
    })
    expect(text).toEqual({ kind: 'assistant-text', text: 'Hello from the mock model.' })
    const thinking = providerRow(AgentProvider.OH_MY_PI, {
      type: 'message_end',
      message: {
        role: 'assistant',
        content: [
          { type: 'thinking', thinking: 'Greet.' },
        ],
      },
    })
    expect(thinking).toEqual({ kind: 'hidden' })
  })

  it('reads LeapMux\'s user row', () => {
    expect(providerRow(AgentProvider.OH_MY_PI, { content: 'say hello' })).toEqual({ kind: 'user', text: 'say hello', attachments: [] })
  })

  it('reads LeapMux\'s plan-execution row', () => {
    expect(providerRow(AgentProvider.OH_MY_PI, { content: 'Run the plan.', planExecution: true })).toEqual({ kind: 'plan-execution', text: 'Run the plan.' })
  })

  it('reads the end of a run as a divider', () => {
    expect(providerRow(AgentProvider.OH_MY_PI, {
      type: 'agent_end',
      isTerminal: true,
      messages: [
        { role: 'assistant', stopReason: 'stop' },
      ],
    })).toMatchObject({ kind: 'divider' })
  })

  it('reads a tool frame as the row of its call', () => {
    const row = providerRow(AgentProvider.OH_MY_PI, {
      type: 'tool_execution_start',
      toolCallId: 'call_1',
      toolName: 'bash',
      args: { command: 'ls' },
    }, { role: 'request' })
    expect(row?.kind).toBe('tool')
    expect(row?.kind === 'tool'
      ? row.call.kind
      : null).toBe('execute')
  })
})

describe('native output without a filesystem pointer', () => {
  it('keeps a valid native result and omits the output path hook', () => {
    const frame = {
      type: 'tool_execution_end',
      toolCallId: 'native-call',
      toolName: 'bash',
      isError: false,
      result: {
        content: [
          {
            type: 'text',
            text: 'native inline preview',
          },
        ],
        details: {
          meta: {
            truncation: {
              artifactId: '0',
            },
          },
        },
      },
    }
    const call = providerToolCall(AgentProvider.OH_MY_PI, frame, {
      spanId: 'native-call',
      spanType: 'bash',
      agentSessionId: 'native-session',
      role: 'result',
    })
    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toBeUndefined()
    const registered = providerFor(AgentProvider.OH_MY_PI)
    expect(registered).toBeDefined()
    expect(registered?.transcript.outputFilePaths).toBeUndefined()
  })
})
