import { AsyncLocalStorage } from 'node:async_hooks'
import { AgentSession } from '@earendil-works/pi-coding-agent'

const bridge = Symbol.for('leapmux.pi.steerIdentity')

function attachInputIdentity() {
  const prompt = AgentSession.prototype.prompt
  if (AgentSession.prototype[bridge] === configuration.capability)
    return
  const owner = new AsyncLocalStorage()
  const linked = new WeakSet()
  const marker = Symbol('leapmux prompt identity')
  function tag(session, message) {
    const active = owner.getStore()
    if (active?.session !== session || !active.input || active.input.used || message.role !== 'user')
      return message
    active.input.used = true
    return { ...message, [configuration.messageField]: active.input.id }
  }
  AgentSession.prototype.prompt = function (text, options) {
    let input = options?.[marker]
    if (!input && options?.source === 'rpc' && options.streamingBehavior === 'steer' && text.startsWith(configuration.prefix)) {
      const boundary = text.indexOf('\n', configuration.prefix.length)
      const id = boundary < 0 ? '' : text.slice(configuration.prefix.length, boundary)
      if (id.length === configuration.idLength && /^[A-Za-z0-9]+$/.test(id)) {
        input = { id, used: false }
        options = { ...options, [marker]: input }
        text = text.slice(boundary + 1)
      }
    }
    if (input && !linked.has(this)) {
      const session = this
      if (typeof session.agent?.steer !== 'function' || typeof session.agent.prompt !== 'function')
        throw new Error('Pi does not expose a queue for steer identity.')
      const steer = session.agent.steer.bind(session.agent)
      const run = session.agent.prompt.bind(session.agent)
      session.agent.steer = message => steer(tag(session, message))
      session.agent.prompt = (messages, ...rest) => run(
        Array.isArray(messages) ? messages.map(message => tag(session, message)) : messages, ...rest,
      )
      linked.add(this)
    }
    // Start a fresh scope for extension-generated inputs, including nested prompts.
    return owner.run({ session: this, input }, () => prompt.call(this, text, options))
  }
  Object.defineProperty(AgentSession.prototype, bridge, { value: configuration.capability })
}

export default function (pi) {
  if (typeof AgentSession?.prototype.prompt !== 'function')
    return
  attachInputIdentity()
  pi.registerCommand(configuration.capability, { handler: async () => {} })
}
