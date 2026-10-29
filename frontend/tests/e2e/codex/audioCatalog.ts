/** A private model catalog that enables the installed native audio input path. */
export function codexAudioModelCatalog(modelID: string) {
  if (!modelID.trim())
    throw new Error('The native audio catalog requires a model ID.')
  return {
    models: [
      {
        slug: modelID,
        display_name: 'Native audio test',
        description: 'A private model catalog for the isolated audio proof.',
        base_instructions: 'Follow the user instructions and process the supplied audio.',
        experimental_supported_tools: [],
        supported_reasoning_levels: ['low', 'medium', 'high'].map(effort => ({ effort, description: effort })),
        default_reasoning_level: 'high',
        shell_type: 'shell_command',
        visibility: 'list',
        supported_in_api: true,
        priority: 1,
        support_verbosity: false,
        default_verbosity: null,
        apply_patch_tool_type: null,
        truncation_policy: { mode: 'tokens', limit: 10000 },
        supports_parallel_tool_calls: false,
        input_modalities: ['text', 'image', 'audio'],
      },
    ],
  }
}
