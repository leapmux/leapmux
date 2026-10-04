import { basename, dirname, isAbsolute, normalize } from 'node:path'
import { isObject } from '../../../src/lib/jsonPick'

/** Read the stdout slot from the exact native shell result and its separate notice blocks. */
export function gooseNativeOutput(frames: readonly unknown[], callId: string): { path: string, excerpt: string } {
  if (!callId)
    throw new Error('The native Goose native output requires an exact shell call ID.')
  const results = frames.filter(isObject).filter(frame => frame.sessionUpdate === 'tool_call_update'
    && frame.toolCallId === callId && frame.status === 'completed')
  const frame = results.length === 1 ? results[0] : undefined
  const raw = isObject(frame?.rawOutput) ? frame.rawOutput : undefined
  const meta = isObject(frame?._meta) ? frame._meta : undefined
  const goose = isObject(meta?.goose) ? meta.goose : undefined
  const tool = isObject(goose?.toolCall) ? goose.toolCall : undefined
  if (tool?.toolName !== 'shell' || tool.extensionName !== 'developer' || raw?.exit_code !== 0 || typeof raw.stdout !== 'string' || raw.stderr !== '' || !Array.isArray(frame?.content))
    throw new Error('The native Goose native output requires one completed stdout-only shell result.')
  const texts = frame.content.map((item) => {
    const block = isObject(item) && item.type === 'content' && isObject(item.content) ? item.content : undefined
    return block?.type === 'text' && typeof block.text === 'string' ? block.text : ''
  })
  const paths = texts.flatMap(text => [...text.matchAll(/\[Output exceeded [^\r\n]+? Full output saved to ([^\r\n]+?)\. Read it with [^\r\n]+? up to 2000 lines at a time\.\]/gu)].map(match => match[1]))
    .filter((path): path is string => typeof path === 'string' && /^stdout-[0-7]$/u.test(basename(path)))
  const path = paths.length === 1 ? paths[0] : undefined
  if (!path || !isAbsolute(path) || normalize(path) !== path || path.includes('\0') || !/^\.tmp[A-Za-z0-9]+$/u.test(basename(dirname(path))))
    throw new Error('The native Goose result has no unique stdout slot-file reference.')
  return { path, excerpt: raw.stdout }
}
