package mongodb

import (
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Collection name constants.
const (
	colOrgs                  = "orgs"
	colUsers                 = "users"
	colSessions              = "user_sessions"
	colOrgMembers            = "org_members"
	colWorkers               = "workers"
	colWorkerAccessGrants    = "worker_access_grants"
	colWorkerNotifications   = "worker_notifications"
	colRegistrations         = "worker_registrations"
	colWorkspaces            = "workspaces"
	colWorkspaceAccess       = "workspace_access"
	colWorkspaceTabs         = "workspace_tabs"
	colWorkspaceLayouts      = "workspace_layouts"
	colWorkspaceSections     = "workspace_sections"
	colWorkspaceSectionItems = "workspace_section_items"
	colOAuthProviders        = "oauth_providers"
	colOAuthStates           = "oauth_states"
	colOAuthTokens           = "oauth_tokens"
	colOAuthUserLinks        = "oauth_user_links"
	colPendingOAuthSignups   = "pending_oauth_signups"
	colMeta                  = "meta"
)

// allCollectionNames returns all non-meta collection names.
func allCollectionNames() []string {
	return []string{
		colOrgs, colUsers, colSessions, colOrgMembers,
		colWorkers, colWorkerAccessGrants, colWorkerNotifications, colRegistrations,
		colWorkspaces, colWorkspaceAccess, colWorkspaceTabs, colWorkspaceLayouts,
		colWorkspaceSections, colWorkspaceSectionItems,
		colOAuthProviders, colOAuthStates, colOAuthTokens, colOAuthUserLinks, colPendingOAuthSignups,
	}
}

// collectionDef describes one MongoDB collection and its indexes.
type collectionDef struct {
	name    string
	indexes []mongo.IndexModel
}

// allCollections returns every collection definition with index specifications.
func allCollections() []collectionDef {
	return []collectionDef{
		{
			name: colOrgs,
			indexes: []mongo.IndexModel{
				{
					Keys: bson.D{{Key: "name", Value: 1}},
					Options: options.Index().SetUnique(true).SetPartialFilterExpression(bson.D{
						{Key: "deleted_at", Value: nil},
					}),
				},
				{Keys: bson.D{{Key: "deleted_at", Value: 1}, {Key: "created_at", Value: -1}}},
			},
		},
		{
			name: colUsers,
			indexes: []mongo.IndexModel{
				{
					Keys: bson.D{{Key: "username", Value: 1}},
					Options: options.Index().SetUnique(true).SetPartialFilterExpression(bson.D{
						{Key: "deleted_at", Value: nil},
					}),
				},
				{
					Keys: bson.D{{Key: "email", Value: 1}},
					Options: options.Index().SetUnique(true).SetPartialFilterExpression(bson.D{
						{Key: "deleted_at", Value: nil},
						{Key: "email", Value: bson.D{{Key: "$gt", Value: ""}}},
					}),
				},
				{Keys: bson.D{{Key: "org_id", Value: 1}}},
				{Keys: bson.D{{Key: "pending_email", Value: 1}}},
				{Keys: bson.D{{Key: "pending_email_token", Value: 1}}},
				{Keys: bson.D{{Key: "deleted_at", Value: 1}, {Key: "created_at", Value: -1}}},
			},
		},
		{
			name: colSessions,
			indexes: []mongo.IndexModel{
				{Keys: bson.D{{Key: "user_id", Value: 1}}},
				{Keys: bson.D{{Key: "expires_at", Value: 1}}},
				{Keys: bson.D{{Key: "expires_at", Value: 1}, {Key: "last_active_at", Value: -1}}},
			},
		},
		{
			// _id = "orgID|userID"
			name: colOrgMembers,
			indexes: []mongo.IndexModel{
				{Keys: bson.D{{Key: "user_id", Value: 1}}},
				{Keys: bson.D{{Key: "org_id", Value: 1}}},
			},
		},
		{
			name: colWorkers,
			indexes: []mongo.IndexModel{
				{Keys: bson.D{{Key: "auth_token", Value: 1}}},
				{Keys: bson.D{{Key: "registered_by", Value: 1}, {Key: "status", Value: 1}, {Key: "created_at", Value: -1}}},
				{Keys: bson.D{{Key: "deleted_at", Value: 1}, {Key: "created_at", Value: -1}}},
			},
		},
		{
			// _id = "workerID|userID"
			name: colWorkerAccessGrants,
			indexes: []mongo.IndexModel{
				{Keys: bson.D{{Key: "user_id", Value: 1}}},
			},
		},
		{
			name: colWorkerNotifications,
			indexes: []mongo.IndexModel{
				{Keys: bson.D{{Key: "worker_id", Value: 1}, {Key: "status", Value: 1}}},
			},
		},
		{
			name: colRegistrations,
			indexes: []mongo.IndexModel{
				{Keys: bson.D{{Key: "status", Value: 1}}},
			},
		},
		{
			name: colWorkspaces,
			indexes: []mongo.IndexModel{
				{Keys: bson.D{{Key: "org_id", Value: 1}, {Key: "owner_user_id", Value: 1}}},
				{Keys: bson.D{{Key: "owner_user_id", Value: 1}}},
			},
		},
		{
			// _id = "workspaceID|userID"
			name: colWorkspaceAccess,
			indexes: []mongo.IndexModel{
				{Keys: bson.D{{Key: "user_id", Value: 1}}},
				{Keys: bson.D{{Key: "workspace_id", Value: 1}}},
			},
		},
		{
			// _id = "workspaceID|tabType|tabID"
			name: colWorkspaceTabs,
			indexes: []mongo.IndexModel{
				{Keys: bson.D{{Key: "worker_id", Value: 1}}},
				{Keys: bson.D{{Key: "workspace_id", Value: 1}}},
			},
		},
		{
			// _id = workspaceID
			name:    colWorkspaceLayouts,
			indexes: nil,
		},
		{
			name: colWorkspaceSections,
			indexes: []mongo.IndexModel{
				{Keys: bson.D{{Key: "user_id", Value: 1}}},
			},
		},
		{
			// _id = "userID|workspaceID"
			name: colWorkspaceSectionItems,
			indexes: []mongo.IndexModel{
				{Keys: bson.D{{Key: "section_id", Value: 1}}},
			},
		},
		{
			name:    colOAuthProviders,
			indexes: nil,
		},
		{
			name: colOAuthStates,
			indexes: []mongo.IndexModel{
				{Keys: bson.D{{Key: "expires_at", Value: 1}}},
			},
		},
		{
			// _id = "userID|providerID"
			name: colOAuthTokens,
			indexes: []mongo.IndexModel{
				{Keys: bson.D{{Key: "provider_id", Value: 1}}},
				{Keys: bson.D{{Key: "key_version", Value: 1}}},
				{Keys: bson.D{{Key: "expires_at", Value: 1}}},
			},
		},
		{
			// _id = "userID|providerID"
			name: colOAuthUserLinks,
			indexes: []mongo.IndexModel{
				{
					Keys:    bson.D{{Key: "provider_id", Value: 1}, {Key: "provider_subject", Value: 1}},
					Options: options.Index().SetUnique(true),
				},
			},
		},
		{
			name: colPendingOAuthSignups,
			indexes: []mongo.IndexModel{
				{Keys: bson.D{{Key: "expires_at", Value: 1}}},
			},
		},
	}
}
