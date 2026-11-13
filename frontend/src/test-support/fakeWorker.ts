import { vi } from 'vitest'

// ---------------------------------------------------------------------------
// Stand-in Workers for the unit tests
//
// jsdom has no Worker, and every worker client of the app spawns one through
// `createWorkerClient`. Two stand-ins take its place:
//
// - AnsweringWorker, which `vitest.setup.ts` installs as the global Worker of
//   every jsdom test. It runs the real request handler of the worker module that
//   the client spawns, on this thread, and answers each message as that worker
//   would. A client therefore takes its browser path, and a test awaits the
//   answer (see settleWorkerReplies). A test that needs a client to wait for a
//   reply holds the replies of that worker module (see holdWorkerReplies), and
//   a test that reads what a client asks records the requests to it (see
//   recordWorkerRequests). A test of the node project runs no setup file, so it
//   stubs AnsweringWorker as the global Worker itself, and after each test it
//   calls what `vitest.setup.ts` calls.
// - CapturingWorker, which a test installs in place of it, through
//   `installCapturingWorkers`. It records each message, and the test answers it by
//   hand, as `reply`. Call `vi.unstubAllGlobals()` after the test, which puts the
//   AnsweringWorker back.
// ---------------------------------------------------------------------------

/** A message event, as a worker client reads it. */
export interface WorkerMessage {
  data: unknown
}

/** A stand-in Worker that records each message that a client posts to it. */
export class CapturingWorker<Req> {
  onmessage: ((event: WorkerMessage) => void) | null = null
  onerror: (() => void) | null = null
  /** Each message that a client posted, in order, as the client posted it. */
  messages: Req[] = []
  terminate = vi.fn()

  postMessage(message: Req): void {
    this.messages.push(message)
  }

  /** Deliver `data` to the client, as the worker's reply. */
  reply(data: unknown): void {
    this.onmessage?.({ data })
  }
}

/**
 * Stub the global Worker with a CapturingWorker, and return each one that a
 * client spawns, in order. Call `vi.unstubAllGlobals()` after the test.
 */
export function installCapturingWorkers<Req>(): CapturingWorker<Req>[] {
  const spawned: CapturingWorker<Req>[] = []
  vi.stubGlobal('Worker', class extends CapturingWorker<Req> {
    constructor() {
      super()
      spawned.push(this)
    }
  })
  return spawned
}

/** The request handler of a worker module: one request in, one reply out. */
type AnswerRequest = (request: never) => unknown

/** Load the request handler of one worker module. */
type LoadHandler = () => Promise<AnswerRequest>

/**
 * The real request handler of each worker module that AnsweringWorker runs,
 * keyed by the path of the module under `src/`. Each handler loads on first use,
 * so a test that spawns no worker loads no worker module.
 *
 * The channel session worker (`~/lib/channelSessionWorker`) has no route on
 * purpose. It holds the keys of every channel, and one command can produce many
 * events, so it does not answer one request with one reply. The tests host its
 * endpoint in process instead (see `~/lib/channelSessionInProcess`).
 */
const HANDLER_LOADERS = {
  'components/chat/diff/diffWorker.ts':
    async () => (await import('~/components/chat/diff/diffWorker')).answerDiffRequest,
  'lib/fieldDigestWorker.ts':
    async () => (await import('~/lib/fieldDigestWorker')).answerFieldDigestRequest,
  'lib/markdownWorker.ts':
    async () => (await import('~/lib/markdownWorker')).answerMarkdownRequest,
  'lib/shikiWorker.ts':
    async () => (await import('~/lib/shikiWorker')).answerTokenizeRequest,
} satisfies Record<string, LoadHandler>

/** The path under `src/` of a worker module that AnsweringWorker answers. */
export type WorkerRoutePath = keyof typeof HANDLER_LOADERS

/** The request that the handler of the worker module at `P` takes. */
export type WorkerRequest<P extends WorkerRoutePath>
  = Parameters<Awaited<ReturnType<(typeof HANDLER_LOADERS)[P]>>>[0]

/** Every key of HANDLER_LOADERS. `Object.keys` types its result as `string[]`. */
const ROUTE_PATHS = Object.keys(HANDLER_LOADERS) as WorkerRoutePath[]

/** One entry of HANDLER_LOADERS. */
interface WorkerRoute {
  path: WorkerRoutePath
  load: LoadHandler
}

/**
 * The route of the worker module at `url`, or undefined when no route answers it.
 *
 * The match reads the path alone. Vite rewrites the URL of each
 * `new Worker(new URL('./x.ts', import.meta.url))` into a path on the document's
 * origin with a query, such as
 * `http://localhost:3000/src/lib/markdownWorker.ts?worker_file&type=module`.
 * A test of the node project has no document, and there the URL is the
 * absolute `file:` URL of the module, which needs no base.
 */
function routeOf(url: string | URL): WorkerRoute | undefined {
  const base = typeof location === 'undefined'
    ? undefined
    : location.href
  const { pathname } = new URL(String(url), base)
  const path = ROUTE_PATHS.find(candidate => pathname.endsWith(`/src/${candidate}`))
  return path === undefined
    ? undefined
    : { path, load: HANDLER_LOADERS[path] }
}

/** The handler of each worker module that a stand-in loaded, keyed like HANDLER_LOADERS. */
const loadedHandlers = new Map<WorkerRoutePath, Promise<AnswerRequest>>()

/** The message handler of the jsdom window, before any worker module replaced it. */
const windowMessageHandler = globalThis.onmessage

/**
 * The handler of the worker module at `path`, loaded once for every stand-in of
 * that module: a module evaluates once per test file anyway.
 *
 * A worker module installs its message handler on `globalThis` when it loads,
 * which here is the jsdom window. The load puts the window's own handler back.
 */
function loadHandler({ path, load }: WorkerRoute): Promise<AnswerRequest> {
  const loaded = loadedHandlers.get(path)
  if (loaded !== undefined)
    return loaded
  const handler = load().finally(() => {
    globalThis.onmessage = windowMessageHandler
  })
  loadedHandlers.set(path, handler)
  return handler
}

/**
 * The answers that a stand-in has not delivered or given to a hold yet (see
 * settleWorkerReplies).
 */
const pendingReplies = new Set<Promise<void>>()

/** An answer of a stand-in, a reply or an error, that a hold keeps from its client. */
interface KeptAnswer {
  worker: AnsweringWorker
  /** Deliver the answer to the client, unless the stand-in is terminated. */
  deliver: () => void
}

/**
 * The replies of one worker module that a test keeps from their clients (see
 * holdWorkerReplies).
 */
export interface WorkerReplyHold {
  /**
   * The number of replies that the hold keeps from their clients now. A reply
   * that the handler still computes does not count yet; `settleWorkerReplies()`
   * waits until the hold keeps it.
   */
  pending: () => number
  /**
   * Deliver the oldest reply that the hold keeps, keep holding the others, and
   * resolve once the client took it in. When the hold keeps no reply, this
   * first waits for each reply that a handler still computes. It rejects when
   * the hold then keeps no reply, or when the hold ended.
   */
  releaseNext: () => Promise<void>
  /**
   * End the hold: deliver every reply that it keeps, in the order in which the
   * handlers computed them, and each later reply as it comes. Resolves once
   * every client took its reply in. A second call does nothing more.
   */
  release: () => Promise<void>
}

/** The hold on the replies of each worker module that a test holds. */
const holds = new Map<WorkerRoutePath, ReplyHold>()

/** The record of the requests to each worker module that a test records. */
const records = new Map<WorkerRoutePath, unknown[]>()

/** A hold on the replies of one worker module. */
class ReplyHold implements WorkerReplyHold {
  /** The kept answers, oldest first. */
  private readonly kept: KeptAnswer[] = []
  private ended = false

  constructor(private readonly path: WorkerRoutePath) {}

  keep(answer: KeptAnswer): void {
    this.kept.push(answer)
  }

  /** Drop every answer of `worker`, which no longer delivers anything. */
  forget(worker: AnsweringWorker): void {
    for (let index = this.kept.length - 1; index >= 0; index--) {
      if (this.kept[index]!.worker === worker)
        this.kept.splice(index, 1)
    }
  }

  pending(): number {
    return this.kept.length
  }

  async releaseNext(): Promise<void> {
    if (this.ended)
      throw new Error(`the hold on the replies of ${this.path} has ended`)
    if (this.kept.length === 0)
      await settleWorkerReplies()
    const next = this.kept.shift()
    if (next === undefined)
      throw new Error(`no reply of ${this.path} is held`)
    next.deliver()
    await settleWorkerReplies()
  }

  async release(): Promise<void> {
    this.end()
    await settleWorkerReplies()
  }

  /** End the hold, and deliver every answer that it keeps, oldest first. */
  end(): void {
    if (this.ended)
      return
    this.ended = true
    holds.delete(this.path)
    for (const answer of this.kept.splice(0))
      answer.deliver()
  }
}

/**
 * A stand-in Worker that answers each message through the real request handler
 * of the worker module that the client spawns, as the worker would:
 * - It clones each message when the client posts it, and the clone of a message
 *   that cannot be cloned throws, as `postMessage` does.
 * - It answers in post order, one message at a time, in a microtask at the
 *   earliest, with a structured clone of the reply.
 * - A handler that throws fires `onerror`, as an uncaught error in a worker does.
 * - A terminated stand-in delivers nothing more.
 * - While a test holds the replies of its worker module (see holdWorkerReplies),
 *   it keeps computing, and the hold keeps each reply and each error from the
 *   client.
 *
 * Every stand-in of one module shares one copy of that module's state, unlike two
 * real workers. The handler also runs in the realm of the page, so a handler must
 * not write the state of the page: each one leaves the syntax theme pair of its
 * isolate to the message handler of its module.
 *
 * Constructing one for a module that has no route throws, as `new Worker` does
 * where no Worker exists, so that client takes its path for a worker that cannot
 * start.
 */
export class AnsweringWorker extends CapturingWorker<unknown> {
  private readonly path: WorkerRoutePath
  private readonly handler: Promise<AnswerRequest>
  /** Settles once the stand-in has answered every message posted so far. */
  private answered: Promise<void> = Promise.resolve()
  private terminated = false
  // The implementation is the one that `vi.fn` takes, so `mockReset` keeps it.
  override terminate = vi.fn(() => {
    this.terminated = true
    for (const hold of holds.values())
      hold.forget(this)
  })

  constructor(url: string | URL) {
    super()
    const route = routeOf(url)
    if (route === undefined)
      throw new Error(`no stand-in Worker answers the worker module ${String(url)}`)
    this.path = route.path
    this.handler = loadHandler(route)
  }

  override postMessage(message: unknown): void {
    const request = structuredClone(message)
    super.postMessage(message)
    records.get(this.path)?.push(message)
    const reply = this.answered.then(async () => {
      const handle = await this.handler
      const data = structuredClone(await handle(request as never))
      this.send(() => this.reply(data))
    })
    const answered = reply.catch((error: unknown) => {
      console.error('[AnsweringWorker] the worker handler failed:', error)
      this.send(() => this.onerror?.())
    })
    this.answered = answered
    pendingReplies.add(answered)
    void answered.finally(() => pendingReplies.delete(answered))
  }

  /** Deliver an answer to the client now, or give it to the hold on this module. */
  private send(deliver: () => void): void {
    if (this.terminated)
      return
    const hold = holds.get(this.path)
    if (hold === undefined) {
      deliver()
      return
    }
    hold.keep({
      worker: this,
      deliver: () => {
        if (!this.terminated)
          deliver()
      },
    })
  }
}

/**
 * Keep each reply of the worker module at `path` from its client until the test
 * releases it. The client posts, and the real handler computes, as without the
 * hold. Only the delivery of the reply waits, so a test can observe a client
 * while it waits for a reply, then release the reply and observe the answer.
 *
 * The hold also keeps each error of the module, and each reply that the module
 * computed before the hold and has not delivered yet. Only one hold per module
 * exists at a time. `vitest.setup.ts` ends every hold after each test (see
 * resetWorkerStandIns).
 */
export function holdWorkerReplies(path: WorkerRoutePath): WorkerReplyHold {
  if (holds.has(path))
    throw new Error(`the replies of ${path} are held already`)
  const hold = new ReplyHold(path)
  holds.set(path, hold)
  return hold
}

/**
 * Record each request that a client posts to a stand-in of the worker module at
 * `path` from now on, and return the record, in post order. The record holds
 * each request as the client posted it, before the clone, so a test can also
 * see whether the client copied a value that it sends.
 *
 * Only one record per module exists at a time. `vitest.setup.ts` stops every
 * record after each test (see resetWorkerStandIns).
 */
export function recordWorkerRequests<P extends WorkerRoutePath>(path: P): readonly WorkerRequest<P>[] {
  if (records.has(path))
    throw new Error(`the requests to ${path} are recorded already`)
  const record: WorkerRequest<P>[] = []
  records.set(path, record)
  return record
}

/**
 * End every hold, deliver each reply that it keeps, and stop every record.
 * `vitest.setup.ts` calls this after each test, before it settles the replies:
 * a client that a test left waiting would otherwise keep its request pending
 * into the next test.
 */
export function resetWorkerStandIns(): void {
  // A copy: each hold that ends removes itself from the map.
  for (const hold of Array.from(holds.values()))
    hold.end()
  records.clear()
}

/** Install AnsweringWorker as the global Worker. `vitest.setup.ts` calls this for every jsdom test. */
export function installAnsweringWorkers(): void {
  Object.defineProperty(globalThis, 'Worker', {
    configurable: true,
    writable: true,
    value: AnsweringWorker,
  })
}

/**
 * Resolve in a task after every queued microtask. A message of a MessageChannel
 * arrives in a task of its own, and fake timers do not replace it, so this also
 * resolves under `vi.useFakeTimers()`, where a zero-delay `setTimeout` never fires.
 */
function nextTask(): Promise<void> {
  return new Promise((resolve) => {
    const { port1, port2 } = new MessageChannel()
    port1.onmessage = () => {
      port1.close()
      resolve()
    }
    port2.postMessage(undefined)
  })
}

/**
 * Resolve once every AnsweringWorker has answered each message posted to it, and
 * each client has taken in its replies. A reply passes through the worker
 * client, a priority gate, a cache and a version bump, each of which takes
 * microtasks of its own, and it can make a client post another message. So this
 * waits for the replies again after each round, until no reply is pending.
 *
 * A reply that a hold keeps (see holdWorkerReplies) counts as answered: this
 * waits until the hold keeps it, not until the test releases it.
 */
export async function settleWorkerReplies(): Promise<void> {
  do {
    await Promise.all(pendingReplies)
    await nextTask()
  } while (pendingReplies.size > 0)
}
