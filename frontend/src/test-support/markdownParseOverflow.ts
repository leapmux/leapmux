import type { MockInstance } from 'vitest'
import { vi } from 'vitest'

/**
 * Make every unified processor that shares `processor`'s prototype raise the
 * RangeError of a stack overflow for `text`, and process every other body as
 * before.
 *
 * The spy sits on the prototype, not on one instance, because every Markdown
 * processor shares that prototype: a real overflow of their shared parser fails
 * them all. A real overflow processes for seconds first, at a depth that depends
 * on the stack of the thread, so a test cannot afford to cause one.
 *
 * Pass the processor of the module instance that the code under test loaded: a
 * test that resets its modules loads a second unified, with a prototype of its
 * own. Returns the spy, so that the test can count the calls for `text`.
 */
export function raiseStackOverflowFor(processor: object, text: string): MockInstance<(value: string) => unknown> {
  const prototype = Object.getPrototypeOf(processor) as { processSync: (value: string) => unknown }
  const process = prototype.processSync
  return vi.spyOn(prototype, 'processSync').mockImplementation(function (this: unknown, value: string) {
    if (value === text)
      throw new RangeError('Maximum call stack size exceeded')
    return process.call(this, value)
  })
}
