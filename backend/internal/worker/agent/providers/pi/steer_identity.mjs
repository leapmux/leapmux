import { AsyncLocalStorage } from 'node:async_hooks'
import { AgentSession } from '@earendil-works/pi-coding-agent'

const bridge = Symbol.for('leapmux.pi.steerIdentity')

function attachInputIdentity() {
  const existing = AgentSession.prototype[bridge]
  if (existing && typeof existing === 'object') {
    existing.config = configuration
    return
  }
  const state = { config: configuration }
  const prompt = AgentSession.prototype.prompt
  const owner = new AsyncLocalStorage()
  const linked = new WeakSet()
  const marker = Symbol('leapmux prompt identity')
  function tag(session, message) {
    const active = owner.getStore()
    if (
      active?.session !== session ||
      !active.input ||
      active.input.used ||
      !message ||
      typeof message !== 'object' ||
      message.role !== 'user'
    ) {
      return message
    }
    active.input.used = true
    message[state.config.messageField] = active.input.id
    return message
  }
  AgentSession.prototype.prompt = function (text, options, ...rest) {
    const config = state.config
    let input = options?.[marker]
    if (
      !input &&
      typeof text === 'string' &&
      options?.source === 'rpc' &&
      options.streamingBehavior === 'steer' &&
      text.startsWith(config.prefix)
    ) {
      const boundary = text.indexOf('\n', config.prefix.length)
      const id = boundary < 0 ? '' : text.slice(config.prefix.length, boundary)
      if (id.length === config.idLength && /^[A-Za-z0-9]+$/.test(id)) {
        input = { id, used: false }
        options = { ...options, [marker]: input }
        text = text.slice(boundary + 1)
      }
    }
    if (input) {
      const session = this
      const currentAgent = session.agent
      if (!currentAgent || typeof currentAgent.steer !== 'function' || typeof currentAgent.prompt !== 'function')
        throw new Error('Pi does not expose a queue for steer identity.')
      if (!linked.has(currentAgent)) {
        const steer = currentAgent.steer.bind(currentAgent)
        const run = currentAgent.prompt.bind(currentAgent)
        currentAgent.steer = (message, ...steerRest) => steer(tag(session, message), ...steerRest)
        currentAgent.prompt = (messages, ...promptRest) => run(
          Array.isArray(messages) ? messages.map(message => tag(session, message)) : tag(session, messages),
          ...promptRest,
        )
        linked.add(currentAgent)
      }
    }
    // Start a fresh scope for extension-generated inputs, including nested prompts.
    return owner.run({ session: this, input }, () => prompt.call(this, text, options, ...rest))
  }
  Object.defineProperty(AgentSession.prototype, bridge, { value: state, configurable: true, writable: true })
}

export default function (pi) {
  if (typeof AgentSession?.prototype.prompt !== 'function')
    return
  attachInputIdentity()
  pi.registerCommand(configuration.capability, { handler: async () => {} })
}
