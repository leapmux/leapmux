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
			pk:   attrID,
			gsis: []gsiDef{
				{name: gsiOrgName, pk: attrName},
				{name: gsiDeletedCreatedAt, pk: attrDeleted, sk: attrCreatedAt},
				{name: gsiDeletedDeletedAt, pk: attrDeleted, sk: attrDeletedAt},
			},
		},
		{
			name: tableUsers,
			pk:   attrID,
			gsis: []gsiDef{
				{name: gsiUsername, pk: attrUsername},
				{name: gsiEmail, pk: attrEmail},
				{name: gsiOrgID, pk: attrOrgID},
				{name: gsiPendingEmailToken, pk: attrPendingEmailToken},
				{name: gsiPendingEmail, pk: attrPendingEmail},
				{name: gsiDeletedCreatedAt, pk: attrDeleted, sk: attrCreatedAt},
				{name: gsiDeletedDeletedAt, pk: attrDeleted, sk: attrDeletedAt},
			},
		},
		{
			name: tableSessions,
			pk:   attrID,
			gsis: []gsiDef{
				{name: gsiUserID, pk: attrUserID},
				{name: gsiNotExpiredLastActiveAt, pk: attrNotExpired, sk: attrLastActiveAt},
				{name: gsiNotExpiredExpiresAt, pk: attrNotExpired, sk: attrExpiresAt},
			},
		},
		{
			name: tableOrgMembers,
			pk:   attrOrgID,
			sk:   attrUserID,
			gsis: []gsiDef{
				{name: gsiUserID, pk: attrUserID},
			},
		},
		{
			name: tableWorkers,
			pk:   attrID,
			gsis: []gsiDef{
				{name: gsiAuthToken, pk: attrAuthToken},
				{name: gsiRegisteredBy, pk: attrRegisteredBy, sk: attrCreatedAt},
				{name: gsiDeletedCreatedAt, pk: attrDeleted, sk: attrCreatedAt},
				{name: gsiDeletedDeletedAt, pk: attrDeleted, sk: attrDeletedAt},
			},
		},
		{
			name: tableWorkerGrants,
			pk:   attrWorkerID,
			sk:   attrUserID,
			gsis: []gsiDef{
				{name: gsiUserID, pk: attrUserID},
			},
		},
		{
			name: tableWorkerNotifications,
			pk:   attrID,
			gsis: []gsiDef{
				{name: gsiWorkerIDStatus, pk: attrWorkerID, sk: attrStatus},
			},
		},
		{
			name: tableRegistrations,
			pk:   attrID,
			gsis: []gsiDef{
				{name: gsiStatus, pk: attrStatus},
			},
		},
		{
			name: tableWorkspaces,
			pk:   attrID,
			gsis: []gsiDef{
				{name: gsiOrgOwner, pk: attrOrgID, sk: attrOwnerUserID},
				{name: gsiOwnerUserID, pk: attrOwnerUserID},
				{name: gsiDeletedDeletedAt, pk: attrDeleted, sk: attrDeletedAt},
			},
		},
		{
			name: tableWorkspaceAccess,
			pk:   attrWorkspaceID,
			sk:   attrUserID,
			gsis: []gsiDef{
				{name: gsiUserID, pk: attrUserID},
			},
		},
		{
			name: tableWorkspaceTabs,
			pk:   attrWorkspaceID,
			sk:   attrTabTypeSK,
			gsis: []gsiDef{
				{name: gsiWorkerID, pk: attrWorkerID},
			},
		},
		{
			name: tableWorkspaceLayouts,
			pk:   attrWorkspaceID,
		},
		{
			name: tableWorkspaceSections,
			pk:   attrID,
			gsis: []gsiDef{
				{name: gsiUserID, pk: attrUserID},
			},
		},
		{
			name: tableWorkspaceSectionItems,
			pk:   attrUserID,
			sk:   attrWorkspaceID,
			gsis: []gsiDef{
				{name: gsiSectionID, pk: attrSectionID},
				{name: gsiWorkspaceID, pk: attrWorkspaceID},
			},
		},
		{
			name: tableOAuthProviders,
			pk:   attrID,
		},
		{
			name: tableOAuthStates,
			pk:   attrState,
			gsis: []gsiDef{
				{name: gsiActiveExpiresAt, pk: attrActive, sk: attrExpiresAt},
			},
		},
		{
			name: tableOAuthTokens,
			pk:   attrUserID,
			sk:   attrProviderID,
			gsis: []gsiDef{
				{name: gsiProviderID, pk: attrProviderID},
				{name: gsiKeyVersion, pk: attrKeyVersion},
				{name: gsiExpiry, pk: attrExpiryPartition, sk: attrExpiresAt},
			},
		},
		{
			name: tableOAuthUserLinks,
			pk:   attrUserID,
			sk:   attrProviderID,
			gsis: []gsiDef{
				{name: gsiProviderSubject, pk: attrProviderID, sk: attrProviderSubject},
			},
		},
		{
			name: tablePendingOAuthSignups,
			pk:   attrToken,
			gsis: []gsiDef{
				{name: gsiActiveExpiresAt, pk: attrActive, sk: attrExpiresAt},
			},
		},
		{
			name: tableUniqueConstraints,
			pk:   attrConstraintValue,
		},
		{
			name: tableMeta,
			pk:   attrKey,
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
