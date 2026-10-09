import type { Page, TestInfo } from '@playwright/test'
import type { MockModelScenarioStatus } from './helpers/mockModelScript'
import type { MockModelServer } from './helpers/mockModelServer'
import type { ModelScript, ModelScriptLifecycle } from './helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext, NativeContextFixtures } from './helpers/nativeScenario'
import { mkdir, mkdtemp, readFile, rm } from 'node:fs/promises'
import { join, resolve } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { finishCleanup, withCleanup } from './helpers/cleanup'
import { MOCK_MODEL_IDS, MODEL_KEY } from './helpers/mockAgentEnvironment'
import { createMockModelServer } from './helpers/mockModelServer'
import { startModelScript } from './helpers/modelScriptFixture'
import { withMuseModelReceipt } from './muse-fixtures'

const registered = vi.hoisted(() => ({ fixtures: undefined as Record<string, unknown> | undefined }))
vi.mock('./fixtures', () => ({
  test: {
    extend: (fixtures: Record<string, unknown>) => {
      registered.fixtures = fixtures
      return {}
    },
  },
}))

type ReceiptReport = Pick<TestInfo, 'outputPath' | 'attach'>

interface NativeFixtureArguments {
  page: Page
  modelScript: ModelScript
  leapmuxServer: NativeContextFixtures['leapmuxServer']
  authenticatedMuseWorkspace: { workspaceId: string }
}

type NativeFixture = (
  args: NativeFixtureArguments,
  use: (context: ManagedNativeScenarioContext) => Promise<void>,
  report: ReceiptReport,
) => Promise<void>

type ReceiptFixture = (
  args: Pick<NativeFixtureArguments, 'modelScript'>,
  use: () => Promise<void>,
  report: ReceiptReport,
) => Promise<void>

const cleanups: (() => Promise<void>)[] = []

afterEach(async () => {
  await finishCleanup(cleanups.splice(0).reverse().map(cleanup => cleanup()))
})

async function receiptRun() {
  const scratch = resolve(import.meta.dirname, '../../.tmp')
  await mkdir(scratch, { recursive: true })
  const directory = await mkdtemp(join(scratch, 'muse-receipt-test-'))
  cleanups.push(() => rm(directory, { recursive: true, force: true }))
  const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
  let lifecycle: ModelScriptLifecycle | undefined
  cleanups.push(() => withCleanup(async () => {
    await lifecycle?.finish(false)
  }, () => server.close()))
  lifecycle = await startModelScript(server.url)
  const attachments: { name: string, path: string, contentType: string | undefined, text: string }[] = []
  const report: ReceiptReport = {
    outputPath: (...files) => join(directory, ...files),
    attach: async (name, options) => {
      if (!options?.path)
        throw new Error('The Muse receipt requires its completed artifact path.')
      attachments.push({ name, path: options.path, contentType: options.contentType, text: await readFile(options.path, 'utf8') })
    },
  }
  return { directory, server, lifecycle, attachments, report }
}

async function completeResponse(server: MockModelServer, lifecycle: ModelScriptLifecycle, text: string, toolResult?: string): Promise<void> {
  await lifecycle.script.queue({ text: 'The receipt turn completed.' })
  const response = await fetch(`${server.url}/v1/responses`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', 'authorization': `Bearer ${MODEL_KEY}` },
    body: JSON.stringify({ model: MOCK_MODEL_IDS[0], stream: false, input: [
      { role: 'user', content: lifecycle.script.prompt(text) },
      ...(toolResult === undefined ? [] : [{ type: 'function_call_output', call_id: 'receipt-call', output: toolResult }]),
    ] }),
  })
  expect(response.status).toBe(200)
  await response.text()
  await lifecycle.script.waitForSteps()
}

async function runRegisteredFixture(
  lifecycle: ModelScriptLifecycle,
  report: ReceiptReport,
  use: (context: ManagedNativeScenarioContext) => Promise<void>,
): Promise<void> {
  const native = registered.fixtures?.native
  if (typeof native !== 'function')
    throw new Error('The Muse native fixture was not registered.')
  const args: NativeFixtureArguments = {
    page: {} as Page,
    modelScript: lifecycle.script,
    leapmuxServer: { hubUrl: 'http://127.0.0.1:1', adminToken: 'test', workerId: 'worker' },
    authenticatedMuseWorkspace: { workspaceId: 'workspace' },
  }
  const run = () => (native as NativeFixture)(args, use, report)
  const receipt = registered.fixtures?.museModelReceipt
  if (Array.isArray(receipt) && typeof receipt[0] === 'function') {
    expect(receipt[1]).toEqual({ auto: true })
    await (receipt[0] as ReceiptFixture)({ modelScript: lifecycle.script }, run, report)
  }
  else {
    await run()
  }
}

describe('museTest', () => {
  it('retains a completed Responses request after its successful native fixture operation', async () => {
    const { server, lifecycle, attachments, report } = await receiptRun()
    let used = false
    await runRegisteredFixture(lifecycle, report, async (context) => {
      expect(context.modelScript).toBe(lifecycle.script)
      await completeResponse(server, lifecycle, 'Keep the actual receipt request for \u754C and zero 0.')
      used = true
    })
    expect(used).toBe(true)
    const request = await lifecycle.script.requestAt(0)
    expect(request.protocol).toBe('openai-responses')
    expect(request.mockCredential).toEqual({ kind: 'bearer', accepted: true })
    expect(attachments, 'the successful Muse fixture retains the actual model receipt').toHaveLength(1)
    const attachment = attachments[0]!
    expect(attachment.name).toBe('muse-model-script')
    expect(attachment.contentType).toBe('application/json')
    const saved = JSON.parse(attachment.text) as MockModelScenarioStatus
    expect(saved.complete).toBe(true)
    expect(saved.requests).toHaveLength(1)
    expect(saved.requests[0]?.body).toEqual(request.body)
    expect(saved.requests[0]?.mockCredential).toEqual({ kind: 'bearer', accepted: true })
    expect(attachment.text).not.toContain(MODEL_KEY)
    expect(await lifecycle.script.status()).toEqual(saved)
  })
})

describe('withMuseModelReceipt', () => {
  it('attaches after the operation and before the script is removed', async () => {
    const { server, lifecycle, report } = await receiptRun()
    const events: string[] = []
    const status = async () => {
      events.push('status')
      return lifecycle.script.status()
    }
    await withMuseModelReceipt({ status }, {
      ...report,
      attach: async (name, options) => {
        events.push('attach')
        await report.attach(name, options)
      },
    }, async () => {
      await completeResponse(server, lifecycle, 'Retain the completed response.')
      events.push('operation')
    })
    expect(events).toEqual(['operation', 'status', 'attach'])
    expect((await lifecycle.script.status()).requests[0]?.response).toMatchObject({ status: 200 })
  })

  it('retains zero model requests without changing script consumption', async () => {
    const { lifecycle, report, attachments } = await receiptRun()
    await withMuseModelReceipt(lifecycle.script, report, async () => {})
    const saved = JSON.parse(attachments[0]!.text) as MockModelScenarioStatus
    expect(saved).toEqual(await lifecycle.script.status())
    expect(saved.requests).toEqual([])
    expect(saved.complete).toBe(true)
  })

  it('retains large Unicode and control text inside actual tool result strings', async () => {
    const { server, lifecycle, report, attachments } = await receiptRun()
    const text = `\u754C\u0000\u001B${'native'.repeat(25_000)}`
    const result = `{"large":9007199254740993,"same":1,"same":2,"text":${JSON.stringify(text)}}`
    await withMuseModelReceipt(lifecycle.script, report, () => completeResponse(server, lifecycle, 'Keep the original tool result.', result))
    const saved = JSON.parse(attachments[0]!.text) as MockModelScenarioStatus
    const original = await lifecycle.script.requestAt(0)
    expect(saved.requests[0]?.body).toEqual(original.body)
    expect(saved.requests[0]?.body).toMatchObject({ input: expect.arrayContaining([
      { type: 'function_call_output', call_id: 'receipt-call', output: result },
    ]) })
    expect(saved.requests[0]?.response).toEqual(original.response)
    expect(attachments[0]!.text).not.toContain(MODEL_KEY)
  })

  it('retains an intentionally unconsumed script without accepting it', async () => {
    const { lifecycle, report, attachments } = await receiptRun()
    await lifecycle.script.queue({ text: 'The pending answer.' })
    lifecycle.script.allowUnconsumed('The test retains its incomplete receipt on purpose.')
    await withMuseModelReceipt(lifecycle.script, report, async () => {})
    const saved = JSON.parse(attachments[0]!.text) as MockModelScenarioStatus
    expect(saved.complete).toBe(false)
    expect(saved.nextStep).toBe(0)
    expect(saved.stepCount).toBe(1)
    expect(saved).toEqual(await lifecycle.script.status())
  })

  it('retains a pending gate before its actual request completes', async () => {
    const { server, lifecycle, report, attachments } = await receiptRun()
    await lifecycle.script.queue({ text: 'The held response.', gate: 'receipt-gate' })
    const response = fetch(`${server.url}/v1/responses`, {
      method: 'POST',
      headers: { 'content-type': 'application/json', 'authorization': `Bearer ${MODEL_KEY}` },
      body: JSON.stringify({ model: MOCK_MODEL_IDS[0], stream: false, input: [{ role: 'user', content: lifecycle.script.prompt('Hold the receipt request.') }] }),
    }).then(async value => ({ status: value.status, text: await value.text() }))
    try {
      await lifecycle.script.waitForGate('receipt-gate')
      await withMuseModelReceipt(lifecycle.script, report, async () => {})
      const saved = JSON.parse(attachments[0]!.text) as MockModelScenarioStatus
      expect(saved.pendingGates).toEqual(['receipt-gate'])
      expect(saved.complete).toBe(false)
      expect(saved.requests).toHaveLength(1)
      expect(saved.requests[0]?.response).toBeUndefined()
    }
    finally {
      await lifecycle.script.releaseGateIfHeld('receipt-gate')
      await response
    }
  })

  it('retains an operation failure and its completed actual request', async () => {
    const { server, lifecycle, report, attachments } = await receiptRun()
    const failure = new Error('The native operation failed.')
    await expect(withMuseModelReceipt(lifecycle.script, report, async () => {
      await completeResponse(server, lifecycle, 'Record the request before the operation fails.')
      throw failure
    })).rejects.toBe(failure)
    expect(attachments).toHaveLength(1)
    const saved = JSON.parse(attachments[0]!.text) as MockModelScenarioStatus
    expect(saved.requests[0]?.body).toEqual((await lifecycle.script.requestAt(0)).body)
    expect(saved.complete).toBe(true)
  })

  it('keeps a status error and writes no empty artifact', async () => {
    const { directory, report, attachments } = await receiptRun()
    const failure = new Error('The script status could not be read.')
    await expect(withMuseModelReceipt({ status: async () => {
      throw failure
    } }, report, async () => {})).rejects.toBe(failure)
    expect(attachments).toEqual([])
    await expect(readFile(join(directory, 'muse-model-script.json'))).rejects.toMatchObject({ code: 'ENOENT' })
  })

  it('writes no successful artifact when status serialization fails', async () => {
    const { directory, lifecycle, report, attachments } = await receiptRun()
    const original = await lifecycle.script.status()
    const circular = Object.assign(original, { circular: original })
    await expect(withMuseModelReceipt({ status: async () => circular }, report, async () => {})).rejects.toBeInstanceOf(TypeError)
    expect(attachments).toEqual([])
    await expect(readFile(join(directory, 'muse-model-script.json'))).rejects.toMatchObject({ code: 'ENOENT' })
  })

  it('keeps an actual file write failure and sends no attachment', async () => {
    const { directory, lifecycle, report, attachments } = await receiptRun()
    const path = join(directory, 'absent-parent', 'muse-model-script.json')
    await expect(withMuseModelReceipt(lifecycle.script, { ...report, outputPath: () => path }, async () => {})).rejects.toMatchObject({ code: 'ENOENT', path })
    expect(attachments).toEqual([])
    await expect(readFile(path)).rejects.toMatchObject({ code: 'ENOENT' })
  })

  it('keeps the completed file and the exact attachment failure', async () => {
    const { directory, lifecycle, report, attachments } = await receiptRun()
    const failure = new Error('The receipt attachment failed.')
    await expect(withMuseModelReceipt(lifecycle.script, { ...report, attach: async () => {
      throw failure
    } }, async () => {})).rejects.toBe(failure)
    expect(attachments).toEqual([])
    expect(JSON.parse(await readFile(join(directory, 'muse-model-script.json'), 'utf8'))).toEqual(await lifecycle.script.status())
  })

  it('keeps operation and receipt errors in their original order', async () => {
    const { report } = await receiptRun()
    const operationFailure = new Error('The operation failed.')
    const receiptFailure = new Error('The status read failed.')
    const outcome = await withMuseModelReceipt({ status: async () => {
      throw receiptFailure
    } }, report, async () => {
      throw operationFailure
    }).catch(error => error)
    expect(outcome).toBeInstanceOf(AggregateError)
    expect(outcome.errors).toEqual([operationFailure, receiptFailure])
  })

  it('keeps concurrent script records in separate private output paths', async () => {
    const first = await receiptRun()
    const second = await receiptRun()
    const results = await Promise.allSettled([
      withMuseModelReceipt(first.lifecycle.script, first.report, () => completeResponse(first.server, first.lifecycle, 'First private receipt.')),
      withMuseModelReceipt(second.lifecycle.script, second.report, () => completeResponse(second.server, second.lifecycle, 'Second private receipt.')),
    ])
    expect(results.map(result => result.status)).toEqual(['fulfilled', 'fulfilled'])
    expect(first.attachments).toHaveLength(1)
    expect(second.attachments).toHaveLength(1)
    expect(first.attachments[0]!.path).not.toBe(second.attachments[0]!.path)
    const savedFirst = JSON.parse(first.attachments[0]!.text) as MockModelScenarioStatus
    const savedSecond = JSON.parse(second.attachments[0]!.text) as MockModelScenarioStatus
    expect(savedFirst).toEqual(await first.lifecycle.script.status())
    expect(savedSecond).toEqual(await second.lifecycle.script.status())
    expect(savedFirst.requests[0]?.body).not.toEqual(savedSecond.requests[0]?.body)
  })
})
