import { basename, dirname, isAbsolute, join, normalize } from 'node:path'

/** Read the separate overflow file that survives foreground Bash completion. */
export function lettaOutputFilePath(text: string, home: string, workingDir: string): string {
  if (!isAbsolute(home) || !isAbsolute(workingDir))
    throw new Error('The native Letta native output requires an absolute private HOME and working directory.')
  const matches = [...text.matchAll(/^\[Full output written to: ([^\r\n]+)\]$/gmu)]
  const path = matches.length === 1 ? matches[0]?.[1] : undefined
  if (!path || !/\[Output truncated: showing [\d,]+ of [\d,]+ characters\.\]/u.test(text))
    throw new Error('The native Letta result requires one native overflow-file reference.')
  const project = normalize(workingDir).replace(/^[/\\]/u, '').replace(/[/\\:]/gu, '_').replace(/\s+/gu, '_')
  const directory = join(home, '.letta', 'projects', project, 'agent-tools')
  if (!isAbsolute(path) || normalize(path) !== path || path.includes('\0') || dirname(path) !== directory
    || !/^bash-[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\.txt$/u.test(basename(path))) {
    throw new Error('The native Letta overflow file belongs to another private project.')
  }
  return path
}
