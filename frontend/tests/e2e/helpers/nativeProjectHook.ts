import { statSync, writeFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'

export interface NativeProjectHook {
  scriptPath: string
  receiptPath: string
}

/** Record native hook input without interpreting any provider's event fields. */
export function writeNativeProjectHook(directory: string, marker: string): NativeProjectHook {
  if (!isAbsolute(directory) || !statSync(directory).isDirectory())
    throw new Error('The native project hook requires an absolute existing directory.')
  if (typeof marker !== 'string' || marker === '')
    throw new Error('The native project hook requires a nonempty marker.')
  const scriptPath = join(directory, 'native-project-hook.cjs')
  const receiptPath = join(directory, 'native-project-hook-receipt.json')
  const source = `
const fs = require('node:fs')
let input = ''
process.stdin.setEncoding('utf8')
process.stdin.on('data', chunk => input += chunk)
process.stdin.once('end', () => {
  try {
    const parsed = JSON.parse(input)
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed))
      throw new Error('The native project hook input must be a JSON object.')
    fs.writeFileSync(${JSON.stringify(receiptPath)}, JSON.stringify({
      marker: ${JSON.stringify(marker)},
      workingDirectory: process.cwd(),
      input: parsed,
    }))
    process.stdout.write(JSON.stringify({ continue: true }))
  }
  catch (error) {
    process.stderr.write('Native project hook failed: ' + String(error.message ?? error) + '\\n')
    process.exitCode = 1
  }
})
`
  writeFileSync(scriptPath, source, { mode: 0o600 })
  return { scriptPath, receiptPath }
}
