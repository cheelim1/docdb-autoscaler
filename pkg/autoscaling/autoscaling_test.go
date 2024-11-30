package autoscaling

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/docdb"
	docdbTypes "github.com/aws/aws-sdk-go-v2/service/docdb/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdsTypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/cheelim1/docdb-autoscaler/pkg/notifications"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"

	// Import the mocks from their respective packages
	mockDocDB "github.com/cheelim1/docdb-autoscaler/pkg/autoscaling/mocks/docdb"
	mockRDS "github.com/cheelim1/docdb-autoscaler/pkg/autoscaling/mocks/rds"
)

// Helper functions to create pointers
func awsString(s string) *string {
	return &s
}

func awsBool(b bool) *bool {
	return &b
}

// Initialize a logger for testing
func getTestLogger() *slog.Logger {
	handler := slog.NewTextHandler(os.Stdout, nil)
	return slog.New(handler)
}

// NoOpNotifier is a dummy notifier that does nothing.
type NoOpNotifier struct{}

func (n *NoOpNotifier) SendScaleOutNotification(clusterID string, replicasAdded int) error {
	return nil
}

func (n *NoOpNotifier) SendScaleInNotification(clusterID string, replicasRemoved int) error {
	return nil
}

func (n *NoOpNotifier) SendFailureNotification(clusterID, errorMessage, action string) error {
	return nil
}

// Ensure NoOpNotifier implements NotifierInterface
var _ notifications.NotifierInterface = (*NoOpNotifier)(nil)

// TestCalculateDesiredCapacity tests the CalculateDesiredCapacity method.
func TestCalculateDesiredCapacity(t *testing.T) {
	docdbAutoScaler := &DocumentDB{
		MinCapacity: 1,
		MaxCapacity: 5,
	}

	tests := []struct {
		name             string
		currentMetric    float64
		currentCapacity  int
		targetValue      float64
		expectedCapacity int
	}{
		{
			name:             "Scale Out",
			currentMetric:    80,
			currentCapacity:  2,
			targetValue:      50,
			expectedCapacity: 4, // ceil(80/50 * 2) = ceil(3.2) = 4
		},
		{
			name:             "Scale In",
			currentMetric:    20,
			currentCapacity:  3,
			targetValue:      50,
			expectedCapacity: 1, // floor(20/50 * 3) = floor(1.2) = 1
		},
		{
			name:             "No Scaling Needed",
			currentMetric:    50,
			currentCapacity:  2,
			targetValue:      50,
			expectedCapacity: 2, // floor(50/50 * 2) = floor(2) = 2
		},
		{
			name:             "Below Min Capacity",
			currentMetric:    10,
			currentCapacity:  2,
			targetValue:      50,
			expectedCapacity: 1, // floor(10/50 * 2) = floor(0.4) = 1 (minCapacity)
		},
		{
			name:             "Above Max Capacity",
			currentMetric:    300,
			currentCapacity:  2,
			targetValue:      50,
			expectedCapacity: 5, // ceil(300/50 * 2) = ceil(12) = 5 (maxCapacity)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			docdbAutoScaler.TargetValue = tt.targetValue
			desired := docdbAutoScaler.CalculateDesiredCapacity(tt.currentMetric, tt.currentCapacity)
			assert.Equal(t, tt.expectedCapacity, desired)
		})
	}
}

// TestExecuteScheduledScalingAction tests the scheduled scaling logic.
func TestExecuteScheduledScalingAction(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockDocDBClient := mockDocDB.NewMockDocDBAPI(ctrl)
	mockRDSClient := mockRDS.NewMockRDSAPI(ctrl)

	docdbAutoScaler := &DocumentDB{
		DocDBClient:            mockDocDBClient,
		RDSClient:              mockRDSClient,
		Logger:                 getTestLogger(),
		ClusterID:              "test-cluster",
		ScheduledScaling:       true,
		ScheduleNumberReplicas: 2,
		MinCapacity:            1,
		MaxCapacity:            5,
		Notifier:               &NoOpNotifier{}, // Initialize Notifier
	}

	// Mock GetReaderInstances
	mockDocDBClient.
		EXPECT().
		DescribeDBInstances(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&docdb.DescribeDBInstancesOutput{
			DBInstances: []docdbTypes.DBInstance{
				{
					DBInstanceIdentifier: awsString("replica-1"),
					DBInstanceArn:        awsString("arn:aws:docdb:region:account-id:db:test-cluster-replica-1"),
					DBInstanceStatus:     awsString("available"),
				},
				{
					DBInstanceIdentifier: awsString("writer-instance"),
					DBInstanceArn:        awsString("arn:aws:docdb:region:account-id:db:test-cluster-writer"),
					DBInstanceStatus:     awsString("available"),
				},
			},
		}, nil).AnyTimes()

	// Mock GetWriterInstanceIdentifier
	mockRDSClient.
		EXPECT().
		DescribeDBClusters(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&rds.DescribeDBClustersOutput{
			DBClusters: []rdsTypes.DBCluster{
				{
					DBClusterIdentifier: awsString("test-cluster"),
					DBClusterMembers: []rdsTypes.DBClusterMember{
						{
							DBInstanceIdentifier: awsString("writer-instance"),
							IsClusterWriter:      awsBool(true),
						},
						{
							DBInstanceIdentifier: awsString("replica-1"),
							IsClusterWriter:      awsBool(false),
						},
					},
				},
			},
		}, nil).AnyTimes()

	// Scenario: No scheduled replicas exist; scaling out
	mockDocDBClient.
		EXPECT().
		ListTagsForResource(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input *docdb.ListTagsForResourceInput, optFns ...func(*docdb.Options)) (*docdb.ListTagsForResourceOutput, error) {
			// No scheduled tags on existing instances
			return &docdb.ListTagsForResourceOutput{
				TagList: []docdbTypes.Tag{},
			}, nil
		}).AnyTimes()

	// Mock CreateDBInstance for adding replicas
	mockDocDBClient.
		EXPECT().
		CreateDBInstance(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input *docdb.CreateDBInstanceInput, optFns ...func(*docdb.Options)) (*docdb.CreateDBInstanceOutput, error) {
			instanceArn := fmt.Sprintf("arn:aws:docdb:region:account-id:db:%s", *input.DBInstanceIdentifier)
			return &docdb.CreateDBInstanceOutput{
				DBInstance: &docdbTypes.DBInstance{
					DBInstanceIdentifier: input.DBInstanceIdentifier,
					DBInstanceArn:        aws.String(instanceArn),
				},
			}, nil
		}).Times(2)

	// Mock AddTagsToResource for tagging new replicas
	mockDocDBClient.
		EXPECT().
		AddTagsToResource(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&docdb.AddTagsToResourceOutput{}, nil).AnyTimes()

	err := docdbAutoScaler.ExecuteScalingAction(context.Background())
	assert.NoError(t, err)
}

// TestExecuteScheduledScalingAction_ScaleIn tests the scheduled scaling logic for scaling in.
func TestExecuteScheduledScalingAction_ScaleIn(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockDocDBClient := mockDocDB.NewMockDocDBAPI(ctrl)
	mockRDSClient := mockRDS.NewMockRDSAPI(ctrl)

	docdbAutoScaler := &DocumentDB{
		DocDBClient:            mockDocDBClient,
		RDSClient:              mockRDSClient,
		Logger:                 getTestLogger(),
		ClusterID:              "test-cluster",
		ScheduledScaling:       true,
		ScheduleNumberReplicas: 2,
		MinCapacity:            1,
		MaxCapacity:            5,
		Notifier:               &NoOpNotifier{}, // Initialize Notifier
	}

	// Mock GetReaderInstances
	mockDocDBClient.
		EXPECT().
		DescribeDBInstances(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&docdb.DescribeDBInstancesOutput{
			DBInstances: []docdbTypes.DBInstance{
				{
					DBInstanceIdentifier: awsString("scheduled-replica-1"),
					DBInstanceArn:        awsString("arn:aws:docdb:region:account-id:db:scheduled-replica-1"),
					DBInstanceStatus:     awsString("available"),
				},
				{
					DBInstanceIdentifier: awsString("writer-instance"),
					DBInstanceArn:        awsString("arn:aws:docdb:region:account-id:db:writer-instance"),
					DBInstanceStatus:     awsString("available"),
				},
			},
		}, nil).AnyTimes()

	// Mock GetWriterInstanceIdentifier
	mockRDSClient.
		EXPECT().
		DescribeDBClusters(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&rds.DescribeDBClustersOutput{
			DBClusters: []rdsTypes.DBCluster{
				{
					DBClusterIdentifier: awsString("test-cluster"),
					DBClusterMembers: []rdsTypes.DBClusterMember{
						{
							DBInstanceIdentifier: awsString("writer-instance"),
							IsClusterWriter:      awsBool(true),
						},
						{
							DBInstanceIdentifier: awsString("scheduled-replica-1"),
							IsClusterWriter:      awsBool(false),
						},
					},
				},
			},
		}, nil).AnyTimes()

	// Mock ListTagsForResource to indicate the replica has the scheduler tag
	mockDocDBClient.
		EXPECT().
		ListTagsForResource(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input *docdb.ListTagsForResourceInput, optFns ...func(*docdb.Options)) (*docdb.ListTagsForResourceOutput, error) {
			if *input.ResourceName == "arn:aws:docdb:region:account-id:db:scheduled-replica-1" {
				return &docdb.ListTagsForResourceOutput{
					TagList: []docdbTypes.Tag{
						{
							Key:   awsString("docdb-autoscaler-scheduler"),
							Value: awsString("true"),
						},
					},
				}, nil
			}
			// No tags for other instances
			return &docdb.ListTagsForResourceOutput{
				TagList: []docdbTypes.Tag{},
			}, nil
		}).AnyTimes()

	// Mock DeleteDBInstance for removing replicas
	mockDocDBClient.
		EXPECT().
		DeleteDBInstance(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&docdb.DeleteDBInstanceOutput{}, nil).Times(1)

	err := docdbAutoScaler.ExecuteScalingAction(context.Background())
	assert.NoError(t, err)
}
