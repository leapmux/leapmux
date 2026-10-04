import { randomUUID } from 'node:crypto'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import { pathToFileURL } from 'node:url'

/** Keep the existing MCP server unchanged and close it through one private file signal. */
export function createMcpCloseControl(directory: string, serverScript: string): { script: string, close: () => void } {
  if (!isAbsolute(directory) || !isAbsolute(serverScript) || !existsSync(serverScript))
    throw new Error('The controlled MCP close requires an absolute directory and an existing server script.')
  const control = join(directory, `mcp-close-${randomUUID()}`)
  mkdirSync(control, { mode: 0o700 })
  const signal = join(control, 'close')
  const script = join(control, 'server.mjs')
  writeFileSync(script, `import {existsSync,watch} from 'node:fs';
const stop=()=>{if(existsSync(${JSON.stringify(signal)}))process.exit(0)};
const watcher=watch(${JSON.stringify(control)},stop);
stop();
await import(${JSON.stringify(pathToFileURL(serverScript).href)});
watcher.close();
`, { mode: 0o600 })
  return { script, close: () => writeFileSync(signal, '', { mode: 0o600 }) }
}
