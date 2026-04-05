import { createClient } from '@connectrpc/connect'
import { AuthService } from '~/generated/leapmux/v1/auth_pb'
import { ChannelService } from '~/generated/leapmux/v1/channel_pb'
import { OrgService } from '~/generated/leapmux/v1/org_pb'
import { SectionService } from '~/generated/leapmux/v1/section_pb'
import { UserService } from '~/generated/leapmux/v1/user_pb'
import { WorkerManagementService } from '~/generated/leapmux/v1/worker_pb'
import { WorkspaceService } from '~/generated/leapmux/v1/workspace_pb'
import { transport } from './transport'

export const authClient = createClient(AuthService, transport)
export const workerClient = createClient(WorkerManagementService, transport)
export const orgClient = createClient(OrgService, transport)
export const userClient = createClient(UserService, transport)
export const sectionClient = createClient(SectionService, transport)
export const channelClient = createClient(ChannelService, transport)
export const workspaceClient = createClient(WorkspaceService, transport)
