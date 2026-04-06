package dynamodb

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// constraintKey builds a unique constraint key like "org_name:myorg".
func constraintKey(entity, field, value string) string {
	return entity + "_" + field + ":" + value
}

// putConstraint returns a TransactWriteItem that inserts a unique constraint.
// The condition ensures the constraint does not already exist.
func putConstraint(table, entity, field, value string) ddbtypes.TransactWriteItem {
	return ddbtypes.TransactWriteItem{
		Put: &ddbtypes.Put{
			TableName: aws.String(table),
			Item: map[string]ddbtypes.AttributeValue{
				"constraint_value": attrS(constraintKey(entity, field, value)),
			},
			ConditionExpression: aws.String("attribute_not_exists(constraint_value)"),
		},
	}
}

// deleteConstraint returns a TransactWriteItem that removes a unique constraint.
func deleteConstraint(table, entity, field, value string) ddbtypes.TransactWriteItem {
	return ddbtypes.TransactWriteItem{
		Delete: &ddbtypes.Delete{
			TableName: aws.String(table),
			Key: map[string]ddbtypes.AttributeValue{
				"constraint_value": attrS(constraintKey(entity, field, value)),
			},
		},
	}
}
