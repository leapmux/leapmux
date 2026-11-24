import { AsyncLocalStorage } from 'node:async_hooks'
import { AgentSession, ExtensionRunner } from '@oh-my-pi/pi-coding-agent'
import { Agent } from '@oh-my-pi/pi-agent-core'

const bridge = Symbol.for('leapmux.ohmypi.steerIdentity')

function readIdentity(text, prefix) {
  if (typeof text !== 'string' || !text.startsWith(prefix))
    return
  const boundary = text.indexOf('\n', prefix.length)
  const id = boundary < 0 ? '' : text.slice(prefix.length, boundary)
  if (id.length === configuration.idLength && /^[A-Za-z0-9]+$/.test(id))
    return { id, text: text.slice(boundary + 1) }
}

function installIdentityBridge() {
  const input = ExtensionRunner?.prototype.emitInput
  const prompt = AgentSession?.prototype.prompt
  const agentPrompt = Agent?.prototype.prompt
  if (typeof input !== 'function' || typeof prompt !== 'function' || typeof agentPrompt !== 'function')
    return false
  if (AgentSession.prototype[bridge] === configuration.capability)
    return true

  const transport = Symbol('leapmux input identity')
  const prompts = new AsyncLocalStorage()
  ExtensionRunner.prototype.emitInput = async function (text, images, source) {
    const identity = source === 'rpc' && readIdentity(text, configuration.prefix)
    if (!identity)
      return input.call(this, text, images, source)

    const result = await input.call(this, identity.text, images, source)
    if (result.handled)
      return result

    // Native RPC passes this array to prompt after running every input hook.
    // The symbol belongs to the array, outside the model's image or text data.
    const forwarded = [...(result.images ?? images ?? [])]
    Object.defineProperty(forwarded, transport, { value: identity.id })
    return { ...result, text: result.text ?? identity.text, images: forwarded }
  }

  function tagUserMessage(message, owner) {
    if (!owner.tagged && message?.role === 'user' && message.attribution === 'user') {
      owner.tagged = true
      message[configuration.messageField] = owner.id
    }
  }

  AgentSession.prototype.prompt = function (text, options) {
    if (options?.streamingBehavior !== 'steer')
      return prompts.run(null, () => prompt.call(this, text, options))
    // steer_subagent calls the child's prompt directly and bypasses RPC input hooks.
    const child = options.throwOnDrop && readIdentity(text, configuration.childPrefix)
    const id = child?.id ?? options.images?.[transport]
    if (!id)
      return prompts.run(null, () => prompt.call(this, text, options))
    const owner = { id, agent: this.agent, tagged: false }
    const onPromptQueued = (messages) => {
      for (const message of messages)
        tagUserMessage(message, owner)
      options.onPromptQueued?.(messages)
    }
    return prompts.run(owner, () => prompt.call(this, child?.text ?? text, { ...options, onPromptQueued }))
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
  Object.defineProperty(AgentSession.prototype, bridge, { value: configuration.capability })
  return true
}

export default function (pi) {
  if (!installIdentityBridge())
    return
  // Native RPC bypasses emitInput when there are zero registered input hooks.
  pi.on('input', () => {})
  pi.registerCommand(configuration.capability, { handler: async () => {} })
}
