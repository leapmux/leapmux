/** Utility functions shared across chat renderers and other components. */

/** Type guard for plain objects. */
export function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

/** Replace a home-directory prefix with ~ */
export function tildify(absPath: string, homeDir?: string): string {
  if (!homeDir)
    return absPath
  const homeNorm = homeDir.endsWith('/') ? homeDir.slice(0, -1) : homeDir
  if (absPath === homeNorm)
    return '~'
  const homeBase = `${homeNorm}/`
  if (absPath.startsWith(homeBase))
    return `~/${absPath.slice(homeBase.length)}`
  return absPath
}

/** Relativize an absolute path against the workspace working directory. */
export function relativizePath(absPath: string, workingDir?: string, homeDir?: string): string {
  if (!workingDir)
    return absPath
  const base = workingDir.endsWith('/') ? workingDir : `${workingDir}/`

  // Candidate 1: direct relative (path is under workingDir)
  if (absPath.startsWith(base)) {
    return absPath.slice(base.length) // always shortest for subpaths
  }

  // Candidate 2: ../ relative path
  const baseParts = base.split('/').filter(Boolean)
  const absParts = absPath.split('/').filter(Boolean)
  let common = 0
  while (common < baseParts.length && common < absParts.length && baseParts[common] === absParts[common])
    common++
  const ups = baseParts.length - common
  const dotRel = '../'.repeat(ups) + absParts.slice(common).join('/')

  // Candidate 3: ~/... tilde path
  const tildePath = tildify(absPath, homeDir)

  // Pick shortest
  let best = absPath
  if (dotRel.length < best.length)
    best = dotRel
  if (tildePath !== absPath && tildePath.length < best.length)
    best = tildePath
  return best
}

/** Extract assistant content array from parsed message, or null if not applicable. */
export function getAssistantContent(parsed: unknown): Array<Record<string, unknown>> | null {
  if (!isObject(parsed) || parsed.type !== 'assistant')
    return null
  const message = parsed.message as Record<string, unknown>
  if (!isObject(message))
    return null
  const content = message.content
  if (!Array.isArray(content))
    return null
  return content as Array<Record<string, unknown>>
}
