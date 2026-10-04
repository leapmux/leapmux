import { Buffer } from 'node:buffer'
import { randomUUID } from 'node:crypto'
import { existsSync, mkdtempSync, statSync, watch, writeFileSync } from 'node:fs'
import { writeFile } from 'node:fs/promises'
import { dirname, isAbsolute, join } from 'node:path'

const FILE_SIGNAL_DEADLINE_MS = 30_000
const NATIVE_OUTPUT_DEADLINE_MS = 600_000

export interface ToolOutputControl {
  command: string
  scriptPath: string
  firstMarker: string
  secondMarker: string
  waitForFirstOutput: () => Promise<void>
  waitForSecondOutput: () => Promise<void>
  releaseFirstOutput: () => Promise<void>
  releaseFinalOutput: () => Promise<void>
}

/** Wait for an actual filesystem signal, including one that arrived before subscription. */
export async function waitForFileSignal(path: string): Promise<void> {
  if (existsSync(path))
    return
  await new Promise<void>((resolve, reject) => {
    const subscription = watch(dirname(path))
    const deadline = setTimeout(() => finish(new Error('The native output signal did not arrive.')), FILE_SIGNAL_DEADLINE_MS)
    let finished = false
    function finish(error?: Error) {
      if (finished)
        return
      finished = true
      clearTimeout(deadline)
      subscription.close()
      if (error)
        reject(error)
      else
        resolve()
    }
    const inspect = () => {
      if (existsSync(path))
        finish()
    }
    subscription.on('change', inspect)
    subscription.on('error', finish)
    inspect()
  })
}

/** Hold a real native command between two independently observable output segments. */
export function createToolOutputControl(workingDir: string, markers?: { first: string, second: string }): ToolOutputControl {
  if (!isAbsolute(workingDir) || !statSync(workingDir).isDirectory())
    throw new Error('The native output command needs an absolute working directory.')
  if (markers && (!markers.first || !markers.second || markers.first === markers.second))
    throw new Error('The native output command needs two distinct nonempty markers.')
  const directory = mkdtempSync(join(workingDir, 'tool-output-control-'))
  const firstReady = join(directory, 'first-ready')
  const secondReady = join(directory, 'second-ready')
  const releaseFirst = join(directory, 'release-first')
  const releaseFinal = join(directory, 'release-final')
  const firstMarker = markers?.first ?? `NATIVEFIRST${randomUUID().replaceAll('-', '')}`
  const secondMarker = markers?.second ?? `NATIVESECOND${randomUUID().replaceAll('-', '')}`
  const source = `
const fs = require('node:fs')
const directory = ${JSON.stringify(directory)}
const releaseFirst = ${JSON.stringify(releaseFirst)}
const releaseFinal = ${JSON.stringify(releaseFinal)}
let secondWritten = false
let finished = false
const deadline = setTimeout(() => {
  finished = true
  process.exitCode = 1
  subscription.close()
}, ${NATIVE_OUTPUT_DEADLINE_MS})
function inspect() {
  if (finished) return
  if (!secondWritten && fs.existsSync(releaseFirst)) {
    secondWritten = true
    fs.writeSync(1, ${JSON.stringify(`${secondMarker}\n${'y'.repeat(4096)}\n`)})
    fs.writeFileSync(${JSON.stringify(secondReady)}, 'ready')
  }
  if (secondWritten && fs.existsSync(releaseFinal)) {
    finished = true
    clearTimeout(deadline)
    subscription.close()
  }
}
const subscription = fs.watch(directory, inspect)
subscription.on('error', error => {
  console.error(error.message)
  process.exitCode = 1
  clearTimeout(deadline)
  subscription.close()
})
fs.writeSync(1, ${JSON.stringify(`${firstMarker}\n${'x'.repeat(4096)}\n`)})
fs.writeFileSync(${JSON.stringify(firstReady)}, 'ready')
inspect()
`
  const scriptPath = join(directory, 'output.cjs')
  writeFileSync(scriptPath, source)
  const encoded = Buffer.from(source).toString('base64')
  return {
    command: `node -e "eval(Buffer.from('${encoded}','base64').toString())"`,
    scriptPath,
    firstMarker,
    secondMarker,
    waitForFirstOutput: () => waitForFileSignal(firstReady),
    waitForSecondOutput: () => waitForFileSignal(secondReady),
    releaseFirstOutput: () => writeFile(releaseFirst, 'continue'),
    releaseFinalOutput: () => writeFile(releaseFinal, 'finish'),
  }
}
