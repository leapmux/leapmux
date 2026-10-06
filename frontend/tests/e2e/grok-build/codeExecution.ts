import { Buffer } from 'node:buffer'
import { lstatSync, realpathSync } from 'node:fs'
import { basename, dirname, isAbsolute, join, relative, sep } from 'node:path'
import { isObject } from '../../../src/lib/jsonPick'
import { acpClosedToolCall } from '../helpers/acpToolFrame'
import { readNativeToolOutputFile } from '../helpers/nativeToolOutputFile'

export interface GrokWorkflowLaunch {
  runId: string
  name: string
  scriptPath: string
}

export interface GrokWorkflowCompletion {
  runId: string
  status: 'completed' | 'failed'
  text: string
}

/** Construct the unique name that the native Rhai metadata receives. */
export function grokWorkflowName(label: 'output' | 'error', marker: string): string {
  const name = `native-code-${label}-${marker.toLowerCase()}`
  if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/u.test(name) || Buffer.byteLength(name) > 64)
    throw new Error('The native Grok workflow name must use lowercase words and at most 64 bytes.')
  return name
}

/** Match the native report label that includes the workflow objective. */
export function grokWorkflowReportLabel(name: string, objective: string): string {
  const label = name.trim()
  if (!label)
    throw new Error('The native Grok report requires its workflow name.')
  const detail = objective.trim()
  return detail ? `${label}: ${detail}` : label
}

/** Read the exact native launch result instead of the requested script. */
export function grokWorkflowLaunch(frames: readonly unknown[], callId: string): GrokWorkflowLaunch {
  if (!callId.trim())
    throw new Error('The native Grok workflow requires an exact launch call ID.')
  const results = frames.filter(isObject).filter(frame => acpClosedToolCall(frame, callId, ['completed']))
  if (results.length !== 1)
    throw new Error('The native Grok workflow requires one exact launch result.')
  const value = results[0]?.rawOutput
  if (!isObject(value) || value.type !== 'Workflow' || typeof value.run_id !== 'string' || !value.run_id.trim()
    || value.task_id !== value.run_id || typeof value.name !== 'string' || !value.name.trim()
    || typeof value.script_path !== 'string' || !isAbsolute(value.script_path)) {
    throw new Error('The native Grok workflow launch has no exact run or script identity.')
  }
  return { runId: value.run_id, name: value.name, scriptPath: value.script_path }
}

/** Read the native final manifest. A running manifest supplies no final result. */
export function grokWorkflowCompletion(value: unknown, launch: GrokWorkflowLaunch): GrokWorkflowCompletion | null {
  if (!isObject(value) || value.version !== 4 || typeof value.script_revision !== 'number'
    || !Number.isSafeInteger(value.script_revision) || value.script_revision < 0 || !isObject(value.state)) {
    throw new Error('The native Grok workflow manifest is invalid.')
  }
  const state = value.state
  if (state.run_id !== launch.runId || state.name !== launch.name)
    throw new Error('The native Grok workflow manifest belongs to another run.')
  if (state.status !== 'complete' && state.status !== 'failed') {
    if (state.status === 'active')
      return null
    throw new Error('The native Grok workflow did not complete or fail.')
  }
  if (!Array.isArray(state.history) || !state.history.every(isObject))
    throw new Error('The native Grok workflow has no completion history.')
  const event = state.status === 'complete' ? 'workflow_completed' : 'workflow_failed'
  const final = state.history.filter(isObject).filter(entry => entry.event === event)
  if (final.length !== 1)
    throw new Error('The native Grok workflow requires one exact completion event.')
  const text = state.status === 'complete' ? state.result_summary : state.pause_message
  if (typeof text !== 'string' || (state.status === 'failed' && final[0]?.detail !== text))
    throw new Error('The native Grok workflow has no matching final output.')
  return { runId: launch.runId, status: state.status === 'complete' ? 'completed' : 'failed', text }
}

/** Validate the native output file against its private profile, session, and run. */
export function grokWorkflowManifestPath(launch: GrokWorkflowLaunch, nativeHome: string, sessionId: string): string {
  if (!nativeHome || !sessionId || !launch.runId || !isAbsolute(launch.scriptPath))
    throw new Error('The native Grok full tool output requires its private session identity.')
  const root = realpathSync(nativeHome)
  const path = relative(root, launch.scriptPath)
  const parts = path.split(sep)
  if (isAbsolute(path) || parts.includes('..') || parts[0] !== 'sessions'
    || parts.at(-4) !== sessionId || parts.at(-3) !== 'workflows'
    || parts.at(-2) !== launch.runId || basename(launch.scriptPath) !== 'script.rhai') {
    throw new Error('The native Grok full tool output belongs to another profile, session, or run.')
  }
  let current = root
  for (const part of parts) {
    current = join(current, part)
    if (lstatSync(current).isSymbolicLink())
      throw new Error('The native Grok full tool output path contains a symbolic link.')
  }
  return join(dirname(launch.scriptPath), 'state.json')
}

/** The largest native workflow manifest that the reader accepts, in bytes. */
const GROK_MANIFEST_MAX_BYTES = 512 * 1024

/**
 * Read the native workflow manifest at `path`, which `grokWorkflowManifestPath` returns.
 *
 * `readNativeToolOutputFile` refuses a symbolic link, a file that is not regular, a file larger than the limit, and a
 * file that changes during the read, on every platform. O_NOFOLLOW alone cannot refuse a link: Windows has no
 * O_NOFOLLOW, and an open there follows a link at the last component of the path.
 */
export function readGrokWorkflowManifest(path: string): unknown {
  return JSON.parse(readNativeToolOutputFile(path, GROK_MANIFEST_MAX_BYTES))
}
