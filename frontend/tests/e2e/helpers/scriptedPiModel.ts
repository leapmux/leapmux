import type { AddressInfo } from 'node:net'
import { Buffer } from 'node:buffer'
import { execFile } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { join } from 'node:path'
import { promisify } from 'node:util'
import { withCleanup } from './cleanup'

/** Drive one real Pi tool call without a remote model or generated arguments. */
export async function withScriptedPiTool<T>(
  directory: string,
  tool: string,
  args: Record<string, unknown>,
  run: (settings: { model: string, optionValues: Record<string, string> }) => Promise<T>,
): Promise<T> {
  let modelRequests = 0
  const server = createServer(async (request, response) => {
    try {
      modelRequests++
      const chunks: Buffer[] = []
      for await (const chunk of request)
        chunks.push(Buffer.from(chunk))
      const body = JSON.parse(Buffer.concat(chunks).toString()) as { messages?: { role?: string }[], tools?: { function?: { name?: string } }[] }
      const finished = body.messages?.some(message => message.role === 'tool')
      if (!finished && !body.tools?.some(item => item.function?.name === tool)) {
        response.writeHead(400).end('The protocol test tool is unavailable.')
        return
      }
      const delta = finished
        ? { role: 'assistant', content: 'Protocol test complete.' }
        : { role: 'assistant', tool_calls: [{ index: 0, id: 'protocol-tool-call', type: 'function', function: { name: tool, arguments: JSON.stringify(args) } }] }
      response.writeHead(200, { 'Content-Type': 'text/event-stream', 'Connection': 'close' })
      for (const value of [
        { id: 'protocol', object: 'chat.completion.chunk', created: 1, model: 'probe', choices: [{ index: 0, delta, finish_reason: null }] },
        { id: 'protocol', object: 'chat.completion.chunk', created: 1, model: 'probe', choices: [{ index: 0, delta: {}, finish_reason: finished ? 'stop' : 'tool_calls' }], usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } },
      ]) {
        response.write(`data: ${JSON.stringify(value)}\n\n`)
      }
      response.end('data: [DONE]\n\n')
    }
    catch {
      response.writeHead(400).end('Invalid protocol test model request.')
    }
  })
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      server.removeListener('error', reject)
      resolve()
    })
  })
  return withCleanup(async () => {
    const port = (server.address() as AddressInfo).port
    const provider = 'leapmux-control-test'
    const configuration = {
      baseUrl: `http://127.0.0.1:${port}/v1`,
      apiKey: 'disposable-protocol-test',
      api: 'openai-completions',
      models: [{ id: 'probe', name: 'Protocol test', reasoning: false, input: ['text'], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 128000, maxTokens: 1000 }],
    }
    const extensions = join(directory, '.pi', 'extensions')
    mkdirSync(extensions, { recursive: true })
    writeFileSync(join(extensions, 'protocol-model.ts'), `export default function (pi) { pi.registerProvider(${JSON.stringify(provider)}, ${JSON.stringify(configuration)}); }`)
    return withCleanup(async () => {
      await setFixtureTrust(directory, true)
      const result = await run({ model: 'probe', optionValues: { pi_provider: provider, effort: 'off' } })
      if (modelRequests < 2)
        throw new Error('The scripted model did not complete the tool exchange.')
      return result
    }, async () => {
      if (existsSync(join(directory, 'prior-project-trust.json')))
        await setFixtureTrust(directory, false)
    })
  }, () => new Promise<void>((resolve, reject) => {
    server.close(error => error ? reject(error) : resolve())
    server.closeAllConnections()
  }))
}

/** Pi loads project extensions only after trust. Restore this temporary directory's exact entry. */
async function setFixtureTrust(directory: string, enable: boolean): Promise<void> {
  const operation = join(directory, enable ? 'protocol-trust-enable.ts' : 'protocol-trust-restore.ts')
  const acknowledgement = join(directory, enable ? 'trust-enabled' : 'trust-restored')
  const previous = join(directory, 'prior-project-trust.json')
  writeFileSync(operation, `
import { getAgentDir, ProjectTrustStore } from '@mariozechner/pi-coding-agent';
import { readFileSync, realpathSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
export default function () {
  const directory = realpathSync(${JSON.stringify(directory)});
  const store = new ProjectTrustStore(getAgentDir());
  if (${JSON.stringify(enable)}) {
    const entry = store.getEntry(directory);
    const previous = entry && resolve(entry.path) === directory ? entry.decision : null;
    writeFileSync(${JSON.stringify(previous)}, JSON.stringify(previous));
    store.set(directory, true);
  } else {
    store.set(directory, JSON.parse(readFileSync(${JSON.stringify(previous)}, 'utf8')));
  }
  writeFileSync(${JSON.stringify(acknowledgement)}, 'ready');
  process.exit(0);
}
`)
  await promisify(execFile)('pi', ['--mode', 'rpc', '--extension', operation], { cwd: directory, timeout: 30_000 })
  if (!existsSync(acknowledgement) || readFileSync(acknowledgement, 'utf8') !== 'ready')
    throw new Error('Pi did not update trust for the protocol test directory.')
}
