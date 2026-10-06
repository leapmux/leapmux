import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('executes a disposable MCP echo tool', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
  void authenticatedOhMyPiWorkspace
  await exerciseMcpEcho(page, modelScript, AgentProvider.OH_MY_PI, 'ohmypi')
})
