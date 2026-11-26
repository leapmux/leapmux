import { AsyncLocalStorage } from 'node:async_hooks'
import { AgentSession, ExtensionRunner } from '@oh-my-pi/pi-coding-agent'
import { Agent } from '@oh-my-pi/pi-agent-core'

const bridge = Symbol.for('leapmux.ohmypi.steerIdentity')

function readIdentity(text, prefix, idLength) {
  if (typeof text !== 'string' || !text.startsWith(prefix))
    return
  const boundary = text.indexOf('\n', prefix.length)
  const id = boundary < 0 ? '' : text.slice(prefix.length, boundary)
  if (id.length === idLength && /^[A-Za-z0-9]+$/.test(id))
    return { id, text: text.slice(boundary + 1) }
}

function installIdentityBridge() {
  const input = ExtensionRunner?.prototype.emitInput
  const prompt = AgentSession?.prototype.prompt
  const agentPrompt = Agent?.prototype.prompt
  if (typeof input !== 'function' || typeof prompt !== 'function' || typeof agentPrompt !== 'function')
    return false
  const existing = AgentSession.prototype[bridge]
  if (existing && typeof existing === 'object') {
    existing.config = configuration
    return true
  }

  const state = { config: configuration }
  const transport = Symbol('leapmux input identity')
  const prompts = new AsyncLocalStorage()
  ExtensionRunner.prototype.emitInput = async function (text, images, source, ...rest) {
    const config = state.config
    const identity = source === 'rpc' && readIdentity(text, config.prefix, config.idLength)
    if (!identity)
      return input.call(this, text, images, source, ...rest)

    const result = await input.call(this, identity.text, images, source, ...rest)
    if (!result || result.handled)
      return result

    // Native RPC passes this array to prompt after running every input hook.
    // The symbol belongs to the array, outside the model's image or text data.
    const hadImages = (result.images ?? images) !== undefined
    const forwarded = [...(result.images ?? images ?? [])]
    Object.defineProperty(forwarded, transport, { value: { id: identity.id, restoreUndefined: !hadImages } })
    return { ...result, text: result.text ?? identity.text, images: forwarded }
  }

  function tagUserMessage(message, owner) {
    if (!owner.tagged && message && typeof message === 'object' && message.role === 'user' && message.attribution === 'user') {
      owner.tagged = true
      message[state.config.messageField] = owner.id
    }
  }

  AgentSession.prototype.prompt = function (text, options, ...rest) {
    const config = state.config
    if (options?.streamingBehavior !== 'steer')
      return prompts.run(null, () => prompt.call(this, text, options, ...rest))
    // steer_subagent calls the child's prompt directly and bypasses RPC input hooks.
    const child = options.throwOnDrop && readIdentity(text, config.childPrefix, config.idLength)
    const carried = options.images?.[transport]
    const id = child?.id ?? carried?.id
    if (!id)
      return prompts.run(null, () => prompt.call(this, text, options, ...rest))
    const owner = { id, agent: this.agent, tagged: false }
    const nextOptions = {
      ...options,
      onPromptQueued(messages, ...queueRest) {
        if (Array.isArray(messages)) {
          for (const message of messages)
            tagUserMessage(message, owner)
        } else {
          tagUserMessage(messages, owner)
        }
        return options.onPromptQueued?.call(this, messages, ...queueRest)
      },
    }
    if (carried?.restoreUndefined)
      nextOptions.images = undefined
    return prompts.run(owner, () => prompt.call(this, child?.text ?? text, nextOptions, ...rest))
  }

  Agent.prototype.prompt = function (messages, ...options) {
    // A steer can start a fresh turn if the running turn ended before admission.
    const owner = prompts.getStore()
    if (owner?.agent === this) {
      for (const message of Array.isArray(messages) ? messages : [messages])
        tagUserMessage(message, owner)
    }
    return agentPrompt.call(this, messages, ...options)
  }
  Object.defineProperty(AgentSession.prototype, bridge, { value: state, configurable: true, writable: true })
  return true
}

export default function (pi) {
  if (!installIdentityBridge())
    return
  // Native RPC bypasses emitInput when there are zero registered input hooks.
  pi.on('input', () => {})
  pi.registerCommand(configuration.capability, { handler: async () => {} })
}
