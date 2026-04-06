// Package dynamodb implements the Hub store backed by Amazon DynamoDB.
package dynamodb

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// tableDef describes one DynamoDB table and its GSIs.
type tableDef struct {
	name string
	pk   string
	sk   string // empty → simple PK
	gsis []gsiDef
}

// gsiDef describes a Global Secondary Index.
type gsiDef struct {
	name string
	pk   string
	sk   string // empty → simple PK
}

// allTables returns every table definition (without prefix applied).
func allTables() []tableDef {
	return []tableDef{
		{
			name: tableOrgs,
			pk:   "id",
			gsis: []gsiDef{
				{name: gsiOrgName, pk: "name"},
				{name: gsiDeletedCreatedAt, pk: "deleted", sk: "created_at"},
				{name: gsiDeletedDeletedAt, pk: "deleted", sk: "deleted_at"},
			},
		},
		{
			name: tableUsers,
			pk:   "id",
			gsis: []gsiDef{
				{name: gsiUsername, pk: "username"},
				{name: gsiEmail, pk: "email"},
				{name: gsiOrgID, pk: "org_id"},
				{name: gsiPendingEmailToken, pk: "pending_email_token"},
				{name: gsiPendingEmail, pk: "pending_email"},
				{name: gsiDeletedCreatedAt, pk: "deleted", sk: "created_at"},
				{name: gsiDeletedDeletedAt, pk: "deleted", sk: "deleted_at"},
			},
		},
		{
			name: tableSessions,
			pk:   "id",
			gsis: []gsiDef{
				{name: gsiUserID, pk: "user_id"},
				{name: gsiNotExpiredLastActiveAt, pk: "not_expired", sk: "last_active_at"},
				{name: gsiNotExpiredExpiresAt, pk: "not_expired", sk: "expires_at"},
			},
		},
		{
			name: tableOrgMembers,
			pk:   "org_id",
			sk:   "user_id",
			gsis: []gsiDef{
				{name: gsiUserID, pk: "user_id"},
			},
		},
		{
			name: tableWorkers,
			pk:   "id",
			gsis: []gsiDef{
				{name: gsiAuthToken, pk: "auth_token"},
				{name: gsiRegisteredBy, pk: "registered_by", sk: "created_at"},
				{name: gsiDeletedCreatedAt, pk: "deleted", sk: "created_at"},
				{name: gsiDeletedDeletedAt, pk: "deleted", sk: "deleted_at"},
			},
		},
		{
			name: tableWorkerGrants,
			pk:   "worker_id",
			sk:   "user_id",
			gsis: []gsiDef{
				{name: gsiUserID, pk: "user_id"},
			},
		},
		{
			name: tableWorkerNotifications,
			pk:   "id",
			gsis: []gsiDef{
				{name: gsiWorkerIDStatus, pk: "worker_id", sk: "status"},
			},
		},
		{
			name: tableRegistrations,
			pk:   "id",
			gsis: []gsiDef{
				{name: gsiStatus, pk: "status"},
			},
		},
		{
			name: tableWorkspaces,
			pk:   "id",
			gsis: []gsiDef{
				{name: gsiOrgOwner, pk: "org_id", sk: "owner_user_id"},
				{name: gsiOwnerUserID, pk: "owner_user_id"},
				{name: gsiDeletedDeletedAt, pk: "deleted", sk: "deleted_at"},
			},
		},
		{
			name: tableWorkspaceAccess,
			pk:   "workspace_id",
			sk:   "user_id",
			gsis: []gsiDef{
				{name: gsiUserID, pk: "user_id"},
			},
		},
		{
			name: tableWorkspaceTabs,
			pk:   "workspace_id",
			sk:   "tab_type#tab_id",
			gsis: []gsiDef{
				{name: gsiWorkerID, pk: "worker_id"},
			},
		},
		{
			name: tableWorkspaceLayouts,
			pk:   "workspace_id",
		},
		{
			name: tableWorkspaceSections,
			pk:   "id",
			gsis: []gsiDef{
				{name: gsiUserID, pk: "user_id"},
			},
		},
		{
			name: tableWorkspaceSectionItems,
			pk:   "user_id",
			sk:   "workspace_id",
			gsis: []gsiDef{
				{name: gsiSectionID, pk: "section_id"},
				{name: gsiWorkspaceID, pk: "workspace_id"},
			},
		},
		{
			name: tableOAuthProviders,
			pk:   "id",
		},
		{
			name: tableOAuthStates,
			pk:   "state",
			gsis: []gsiDef{
				{name: gsiActiveExpiresAt, pk: "active", sk: "expires_at"},
			},
		},
		{
			name: tableOAuthTokens,
			pk:   "user_id",
			sk:   "provider_id",
			gsis: []gsiDef{
				{name: gsiProviderID, pk: "provider_id"},
				{name: gsiKeyVersion, pk: "key_version"},
				{name: gsiExpiry, pk: "expiry_partition", sk: "expires_at"},
			},
		},
		{
			name: tableOAuthUserLinks,
			pk:   "user_id",
			sk:   "provider_id",
			gsis: []gsiDef{
				{name: gsiProviderSubject, pk: "provider_id", sk: "provider_subject"},
			},
		},
		{
			name: tablePendingOAuthSignups,
			pk:   "token",
			gsis: []gsiDef{
				{name: gsiActiveExpiresAt, pk: "active", sk: "expires_at"},
			},
		},
		{
			name: tableUniqueConstraints,
			pk:   "constraint_value",
		},
		{
			name: tableMeta,
			pk:   "key",
		},
	}
}

// buildCreateTableInput builds the CreateTable input for one tableDef.
func buildCreateTableInput(prefix string, td tableDef) *dynamodb.CreateTableInput {
	tableName := prefix + td.name

	// Key schema
	keySchema := []ddbtypes.KeySchemaElement{
		{AttributeName: aws.String(td.pk), KeyType: ddbtypes.KeyTypeHash},
	}

	// Attribute definitions — collect unique attribute names
	attrSet := map[string]bool{td.pk: true}
	if td.sk != "" {
		keySchema = append(keySchema, ddbtypes.KeySchemaElement{
			AttributeName: aws.String(td.sk),
			KeyType:       ddbtypes.KeyTypeRange,
		})
		attrSet[td.sk] = true
	}

	var gsis []ddbtypes.GlobalSecondaryIndex
	for _, g := range td.gsis {
		attrSet[g.pk] = true
		gsiKeySchema := []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String(g.pk), KeyType: ddbtypes.KeyTypeHash},
		}
		if g.sk != "" {
			attrSet[g.sk] = true
			gsiKeySchema = append(gsiKeySchema, ddbtypes.KeySchemaElement{
				AttributeName: aws.String(g.sk),
				KeyType:       ddbtypes.KeyTypeRange,
			})
		}
		gsis = append(gsis, ddbtypes.GlobalSecondaryIndex{
			IndexName: aws.String(g.name),
			KeySchema: gsiKeySchema,
			Projection: &ddbtypes.Projection{
				ProjectionType: ddbtypes.ProjectionTypeAll,
			},
		})
	}

	var attrDefs []ddbtypes.AttributeDefinition
	for name := range attrSet {
		attrDefs = append(attrDefs, ddbtypes.AttributeDefinition{
			AttributeName: aws.String(name),
			AttributeType: ddbtypes.ScalarAttributeTypeS,
		})
	}

	input := &dynamodb.CreateTableInput{
		TableName:            aws.String(tableName),
		KeySchema:            keySchema,
		AttributeDefinitions: attrDefs,
		BillingMode:          ddbtypes.BillingModePayPerRequest,
	}
	if len(gsis) > 0 {
		input.GlobalSecondaryIndexes = gsis
	}
	return input
}
