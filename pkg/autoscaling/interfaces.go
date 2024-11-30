package autoscaling

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/docdb"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

// DocDBAPI defines the interface for Amazon DocumentDB interactions.
type DocDBAPI interface {
	DescribeDBInstances(ctx context.Context, params *docdb.DescribeDBInstancesInput, optFns ...func(*docdb.Options)) (*docdb.DescribeDBInstancesOutput, error)
	CreateDBInstance(ctx context.Context, params *docdb.CreateDBInstanceInput, optFns ...func(*docdb.Options)) (*docdb.CreateDBInstanceOutput, error)
	DeleteDBInstance(ctx context.Context, params *docdb.DeleteDBInstanceInput, optFns ...func(*docdb.Options)) (*docdb.DeleteDBInstanceOutput, error)
	ListTagsForResource(ctx context.Context, params *docdb.ListTagsForResourceInput, optFns ...func(*docdb.Options)) (*docdb.ListTagsForResourceOutput, error)
	AddTagsToResource(ctx context.Context, params *docdb.AddTagsToResourceInput, optFns ...func(*docdb.Options)) (*docdb.AddTagsToResourceOutput, error)
}

// CloudWatchAPI defines the interface for Amazon CloudWatch interactions.
type CloudWatchAPI interface {
	GetMetricStatistics(ctx context.Context, params *cloudwatch.GetMetricStatisticsInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricStatisticsOutput, error)
}

// RDSAPI defines the interface for Amazon RDS interactions (used for DocumentDB cluster operations).
type RDSAPI interface {
	DescribeDBClusters(ctx context.Context, params *rds.DescribeDBClustersInput, optFns ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error)
}
