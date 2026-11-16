import type { CommandExit } from '../../../model/commandResult'
import type { McpContentItem } from '../../../model/mcpToolCall'
import type { ToolCall, ToolCallSpec } from '../../../model/toolCall'
import type { OpenCodeToolRecord } from './toolCommon'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { OPENCODE_NATIVE_TOOL_STATUS } from '~/generated/contracts/opencode-native-protocol'
import { parseDataImageUrl, withFallbackFilePath } from '~/lib/imageBlocks'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { fileEditDiffFromUnifiedPatch } from '../../../model/fileEditDiff'
import { mcpToolCallRequest } from '../../../model/mcpToolCall'
import { failedResult, proseResult, unparsedResult } from '../../../model/toolCall'
import { rawTodosToItems } from '../../../normalizers/todo'
import { declinedToolCallSpec } from '../../declinedToolCall'
import { toolRequestFor } from '../../defaultToolRequests'
import { questionsFromRecords } from '../../questionRecords'
import { retainedOutcome } from '../../registry'
import { OPENCODE_DECLINED_TOOL_ERRORS } from '../protocol'
import { openCodeToolKind } from '../toolKinds'
import { OPENCODE_TOOL_NAMES } from '../toolNames'
import { openCodeReadResult } from './read'
import { openCodeSearchLines } from './search'
import { openCodeToolText } from './toolCommon'

interface OpenCodeToolRow {
  record: OpenCodeToolRecord
  spanId: string
  rowFinal: boolean
  completion?: MessageCompletion
}

/** Build one correlated tool call from resolved native records. */
export function openCodeToolCall(row: OpenCodeToolRow): ToolCall {
  // A recovered projection can carry finished state on both stored rows.
  // The row's own phase controls whether it exposes outcome and artifact facts.
  const record: OpenCodeToolRecord = row.rowFinal
    ? row.record
    : {
        ...row.record,
        status: OPENCODE_NATIVE_TOOL_STATUS.Running,
        content: [],
        error: null,
      }
  const message = pickString(record.error, 'message')
  const declined = pickString(record.error, 'type') === 'aborted' && OPENCODE_DECLINED_TOOL_ERRORS.has(message)
  const interrupted = !declined && pickString(record.error, 'type') === 'aborted'
  const answered = row.rowFinal
  const output = answered
    ? openCodeToolText(record) || pickString(record.metadata, 'output')
    : ''
  const text = [output, message].filter(Boolean).join('\n\n')
  const failed = answered && (record.status === OPENCODE_NATIVE_TOOL_STATUS.Error
    || (record.name === OPENCODE_TOOL_NAMES.Execute && record.metadata.error === true))
  const built = openCodeSpec(record, answered, failed, output, text)
  const spec = declined
    ? declinedToolCallSpec(built, message)
    : built
  return createToolCall({
    id: row.spanId,
    name: record.name,
    lifecycle: {
      frameStatus: failed
        ? 'failed'
        : record.status === OPENCODE_NATIVE_TOOL_STATUS.Completed
          ? 'completed'
          : 'in_progress',
      providerOutcome: declined
        ? 'declined'
        : interrupted
          ? 'interrupted'
          : failed ? 'failed' : null,
      retainedOutcome: retainedOutcome(row.completion),
      rowFinal: row.rowFinal,
      resultFrameLanded: answered && record.status !== OPENCODE_NATIVE_TOOL_STATUS.Running,
    },
  }, spec)
}

function nativeImages(record: OpenCodeToolRecord): ImageResultSource[] {
  return record.content.flatMap((item) => {
    if (!isObject(item) || item.type !== 'file')
      return []
    const image = parseDataImageUrl(pickString(item, 'uri'))
    return image?.mimeType.startsWith('image/')
      ? [
          { mimeType: image.mimeType, data: image.base64 },
        ]
      : []
  })
}

function nativeContent(record: OpenCodeToolRecord): McpContentItem[] {
  return record.content.map((item): McpContentItem => {
    if (!isObject(item))
      return { type: 'unknown', raw: item }
    if (item.type === 'text' && typeof item.text === 'string')
      return { type: 'text', text: item.text }
    if (item.type === 'file' && typeof item.uri === 'string') {
      const image = parseDataImageUrl(item.uri)
      if (image?.mimeType.startsWith('image/')) {
        return {
          type: 'image',
          source: { mimeType: image.mimeType, data: image.base64 },
        }
      }
      const mimeType = pickString(item, 'mime')
      return {
        type: 'resource',
        uri: item.uri,
        ...(mimeType
          ? { mimeType }
          : {}),
      }
    }
    return { type: 'unknown', raw: item }
  })
}

function openCodeSpec(record: OpenCodeToolRecord, answered: boolean, failed: boolean, output: string, text: string): ToolCallSpec {
  const args = record.input
  const kind = openCodeToolKind(record.name)
  const failure = failedResult(text || 'The tool failed.')
  switch (kind) {
    case 'todo':
      return {
        kind,
        request: {
          items: rawTodosToItems(args.todos),
        },
        ...(answered
          ? {
              result: failed
                ? failure
                : Array.isArray(record.metadata.todos)
                  ? {
                      items: rawTodosToItems(record.metadata.todos),
                    }
                  : unparsedResult(output),
            }
          : {}),
      }
    case 'execute': {
      const code = record.name === OPENCODE_TOOL_NAMES.Execute
      const command = pickString(args, code ? 'code' : 'command')
      const cwd = pickString(args, 'workdir')
      const exitCode = pickNumber(record.metadata, 'exit', undefined)
      const exit: CommandExit = exitCode !== undefined
        ? { exitCode }
        : failed
          ? { failed: true }
          : {}
      return {
        kind,
        images: nativeImages(record),
        request: {
          command,
          language: code ? 'javascript' : 'bash',
          ...(cwd
            ? { cwd }
            : {}),
        },
        ...(answered
          ? {
              result: failed && !output
                ? failure
                : {
                    commands: [
                      {
                        output: text,
                        ...exit,
                      },
                    ],
                    unresolvedTerminals: [],
                  },
            }
          : {}),
      }
    }
    case 'read': {
      const request = toolRequestFor(kind, args, undefined, {})
      const images = nativeImages(record).map(image => withFallbackFilePath(image, request.path))
      return {
        kind,
        request,
        ...(answered
          ? {
              result: failed
                ? failure
                : openCodeReadResult(output) ?? unparsedResult(output),
              images,
            }
          : {}),
      }
    }
    case 'edit':
    case 'write': {
      const request = toolRequestFor(kind, args, undefined, {})
      if (request.changes.length === 0)
        return genericSpec(record, answered, failed, text)
      const changes = Array.isArray(record.metadata.files)
        ? record.metadata.files.flatMap((item) => {
            if (!isObject(item))
              return []
            const path = pickString(item, 'file')
            const patch = fileEditDiffFromUnifiedPatch(path, pickString(item, 'patch'))
            return path && patch
              ? [patch]
              : []
          })
        : []
      return {
        kind,
        request,
        ...(answered
          ? {
              result: failed
                ? failure
                : changes.length > 0
                  ? { changes }
                  : unparsedResult(output),
            }
          : {}),
      }
    }
    case 'glob':
    case 'grep': {
      const request = toolRequestFor(kind, args, undefined, {})
      if (!answered)
        return { kind, request }
      if (failed)
        return { kind, request, result: failure }
      const truncated = record.metadata.truncated === true
      const count = pickNumber(record.metadata, kind === 'glob'
        ? 'count'
        : 'matches', undefined)
      const lines = kind === 'grep' && count !== undefined
        ? openCodeSearchLines(output, count, truncated)
        : null
      const filenames = kind === 'glob' && count !== undefined && Number.isSafeInteger(count) && count >= 0
        ? output.trim() === 'No files found' && count === 0
          ? []
          : output.split(/\r?\n/).filter(Boolean)
        : lines
          ? [
              ...new Set(lines.map(line => line.filePath)),
            ]
          : null
      if (!filenames || (kind === 'glob' && filenames.length !== count) || (kind === 'grep' && !lines)) {
        return {
          kind,
          request,
          result: unparsedResult(output),
        }
      }
      return {
        kind,
        request,
        result: {
          filenames,
          content: kind === 'grep'
            ? output
            : '',
          numFiles: filenames.length,
          numLines: lines?.length ?? 0,
          ...(lines
            ? { lines }
            : {}),
          ...(count !== undefined
            ? { matchCount: count }
            : {}),
          truncated,
          fallbackContent: output,
          empty: count === 0,
        },
      }
    }
    case 'agent': {
      const registryKey = pickString(record.metadata, 'sessionID') || pickString(args, 'sessionID')
      const description = pickString(args, 'description')
      const agentType = pickString(args, 'agent')
      return {
        kind,
        request: {
          description,
          prompt: pickString(args, 'prompt'),
          ...(agentType
            ? { agentType }
            : {}),
          ...(registryKey
            ? { registryKey }
            : {}),
        },
        ...(answered
          ? {
              result: failed
                ? failure
                : registryKey
                  ? {
                      agents: [
                        {
                          agentId: registryKey,
                          registryKey,
                          description,
                          outcome: 'running' as const,
                          metadata: [],
                          body: output,
                        },
                      ],
                    }
                  : unparsedResult(output),
            }
          : {}),
      }
    }
    case 'question':
      return {
        kind,
        request: {
          questions: questionsFromRecords(
            args.questions,
            (question) => {
              const header = pickString(question, 'header')
              return {
                question: pickString(question, 'question'),
                ...(header
                  ? { header }
                  : {}),
              }
            },
            (option) => {
              const label = pickString(option, 'label')
              if (!label)
                return null
              const description = pickString(option, 'description')
              return {
                label,
                ...(description
                  ? { description }
                  : {}),
              }
            },
          ),
        },
        ...(answered
          ? {
              result: failed
                ? failure
                : {
                    answers: [],
                    ...(output
                      ? { note: output }
                      : {}),
                  },
            }
          : {}),
      }
    case 'skill': {
      const name = pickString(args, 'id')
      return {
        kind,
        request: name
          ? { name }
          : {},
        ...(answered
          ? {
              result: failed
                ? failure
                : proseResult(output, 'markdown'),
            }
          : {}),
      }
    }
    case 'fetch':
      return {
        kind,
        request: toolRequestFor(kind, args, undefined, {}),
        ...(answered
          ? {
              result: failed
                ? failure
                : { result: output },
            }
          : {}),
      }
    case 'web_search':
      return {
        kind,
        request: toolRequestFor(kind, args, undefined, {}),
        ...(answered
          ? {
              result: failed
                ? failure
                : unparsedResult(output),
            }
          : {}),
      }
    case 'mcp':
      return genericSpec(record, answered, failed, text)
  }
}

function genericSpec(record: OpenCodeToolRecord, answered: boolean, failed: boolean, text: string): ToolCallSpec<'mcp'> {
  return {
    ...mcpToolCallRequest('', record.name, record.input),
    ...(answered
      ? {
          result: failed
            ? failedResult(text || 'The tool failed.')
            : {
                content: nativeContent(record),
              },
        }
      : {}),
  }
}
