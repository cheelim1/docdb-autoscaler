package autoscaling

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwTypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/docdb"
	docdbTypes "github.com/aws/aws-sdk-go-v2/service/docdb/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/cheelim1/docdb-autoscaler/pkg/notifications"
)

// DocumentDB represents the DocumentDB cluster configuration and state.
type DocumentDB struct {
	ClusterID              string
	MinCapacity            int
	MaxCapacity            int
	MetricName             string
	TargetValue            float64
	ScaleInCooldown        int
	ScaleOutCooldown       int
	InstanceType           string // Combined instance type and size, e.g., "db.r6g.large"
	DryRun                 bool
	ScheduledScaling       bool
	ScheduleNumberReplicas int

	DocDBClient      DocDBAPI
	CloudWatchClient CloudWatchAPI
	RDSClient        RDSAPI
	Notifier         notifications.NotifierInterface
	Logger           *slog.Logger

	// lastScaleInTime  time.Time
	// lastScaleOutTime time.Time
}

// NewDocumentDB initializes a new DocumentDB instance.
func NewDocumentDB(
	clusterID string,
	minCapacity, maxCapacity int,
	metricName string,
	targetValue float64,
	scaleInCooldown, scaleOutCooldown int,
	instanceType string,
	dryRun bool,
	scheduledScaling bool,
	scheduleNumberReplicas int,
	docdbClient DocDBAPI,
	cloudwatchClient CloudWatchAPI,
	notifier notifications.NotifierInterface,
	logger *slog.Logger,
	rdsClient RDSAPI,
) *DocumentDB {
	return &DocumentDB{
		ClusterID:              clusterID,
		MinCapacity:            minCapacity,
		MaxCapacity:            maxCapacity,
		MetricName:             metricName,
		TargetValue:            targetValue,
		ScaleInCooldown:        scaleInCooldown,
		ScaleOutCooldown:       scaleOutCooldown,
		InstanceType:           instanceType,
		DryRun:                 dryRun,
		ScheduledScaling:       scheduledScaling,
		ScheduleNumberReplicas: scheduleNumberReplicas,
		DocDBClient:            docdbClient,
		CloudWatchClient:       cloudwatchClient,
		RDSClient:              rdsClient,
		Notifier:               notifier,
		Logger:                 logger,
	}
}

// CalculateDesiredCapacity calculates the desired number of read replicas.
func (d *DocumentDB) CalculateDesiredCapacity(currentMetricValue float64, currentCapacity int) int {
	proportionalCapacity := (currentMetricValue / d.TargetValue) * float64(currentCapacity)
	var desiredCapacity float64

	if proportionalCapacity > float64(currentCapacity) {
		// Scaling Out: Round up to ensure sufficient capacity
		desiredCapacity = math.Ceil(proportionalCapacity)
	} else {
		// Scaling In: Round down to reduce replicas conservatively
		desiredCapacity = math.Floor(proportionalCapacity)
	}

	// Enforce minimum and maximum bounds
	if desiredCapacity < float64(d.MinCapacity) {
		desiredCapacity = float64(d.MinCapacity)
	} else if desiredCapacity > float64(d.MaxCapacity) {
		desiredCapacity = float64(d.MaxCapacity)
	}

	return int(desiredCapacity)
}

// GetCurrentMetricValue retrieves the current value of the specified CloudWatch metric, considering only reader instances.
func (d *DocumentDB) GetCurrentMetricValue(ctx context.Context) (float64, error) {
	// Step 1: Get all reader instances
	readerInstances, err := d.GetReaderInstances(ctx)
	if err != nil {
		return 0, err
	}

	if len(readerInstances) == 0 {
		return 0, errors.New("no reader instances found")
	}

	var totalMetric float64
	for _, instance := range readerInstances {
		// Step 2: Fetch metric for each reader instance
		input := &cloudwatch.GetMetricStatisticsInput{
			Namespace:  aws.String("AWS/DocDB"),
			MetricName: aws.String(d.MetricName),
			Dimensions: []cwTypes.Dimension{
				{
					Name:  aws.String("DBInstanceIdentifier"),
					Value: instance.DBInstanceIdentifier,
				},
			},
			StartTime:  aws.Time(time.Now().Add(-5 * time.Minute)),
			EndTime:    aws.Time(time.Now()),
			Period:     aws.Int32(300), // 5 minutes
			Statistics: []cwTypes.Statistic{cwTypes.StatisticAverage},
		}

		resp, err := d.CloudWatchClient.GetMetricStatistics(ctx, input)
		if err != nil {
			d.Logger.Error("Failed to get metric statistics", "Error", err, "InstanceID", aws.ToString(instance.DBInstanceIdentifier))
			return 0, err
		}

		if len(resp.Datapoints) == 0 {
			d.Logger.Error("No datapoints found for instance", "InstanceID", aws.ToString(instance.DBInstanceIdentifier))
			return 0, fmt.Errorf("no datapoints found for instance %s", aws.ToString(instance.DBInstanceIdentifier))
		}

		// Sort datapoints by timestamp
		sort.Slice(resp.Datapoints, func(i, j int) bool {
			return resp.Datapoints[i].Timestamp.Before(*resp.Datapoints[j].Timestamp)
		})

		// Use the latest datapoint
		latestDatapoint := resp.Datapoints[len(resp.Datapoints)-1]
		totalMetric += aws.ToFloat64(latestDatapoint.Average)
	}

	// Step 3: Calculate average across readers
	averageMetric := totalMetric / float64(len(readerInstances))
	return averageMetric, nil
}

// GetReaderInstances retrieves all reader instances in the cluster.
func (d *DocumentDB) GetReaderInstances(ctx context.Context) ([]docdbTypes.DBInstance, error) {
	// Get all instances in the cluster
	describeInstancesInput := &docdb.DescribeDBInstancesInput{
		Filters: []docdbTypes.Filter{
			{
				Name:   aws.String("db-cluster-id"),
				Values: []string{d.ClusterID},
			},
		},
	}
	dbInstancesOutput, err := d.DocDBClient.DescribeDBInstances(ctx, describeInstancesInput)
	if err != nil {
		d.Logger.Error("Failed to describe DB instances", "Error", err)
		return nil, err
	}
	dbInstances := dbInstancesOutput.DBInstances

	// Get the writer instance identifier
	writerInstanceIdentifier, err := d.GetWriterInstanceIdentifier(ctx)
	if err != nil {
		d.Logger.Error("Failed to get writer instance identifier", "Error", err)
		return nil, err
	}

	var readerInstances []docdbTypes.DBInstance
	for _, instance := range dbInstances {
		if aws.ToString(instance.DBInstanceIdentifier) == writerInstanceIdentifier {
			continue // Skip the writer instance
		}
		readerInstances = append(readerInstances, instance)
	}

	return readerInstances, nil
}

// GetCurrentCapacity calculates the current number of reader instances in the cluster.
func (d *DocumentDB) GetCurrentCapacity(ctx context.Context) (int, error) {
	readerInstances, err := d.GetReaderInstances(ctx)
	if err != nil {
		return 0, err
	}

	capacity := len(readerInstances)
	d.Logger.Info("Retrieved current capacity", "CurrentCapacity", capacity)
	return capacity, nil
}

// GetWriterInstanceIdentifier retrieves the identifier of the writer (primary) instance.
func (d *DocumentDB) GetWriterInstanceIdentifier(ctx context.Context) (string, error) {
	// Get cluster details
	describeClustersInput := &rds.DescribeDBClustersInput{
		DBClusterIdentifier: aws.String(d.ClusterID),
	}
	dbClustersOutput, err := d.RDSClient.DescribeDBClusters(ctx, describeClustersInput)
	if err != nil {
		d.Logger.Error("Failed to describe DB clusters", "Error", err)
		return "", err
	}
	if len(dbClustersOutput.DBClusters) == 0 {
		return "", fmt.Errorf("no clusters found with identifier %s", d.ClusterID)
	}
	dbCluster := dbClustersOutput.DBClusters[0]

	// Find the writer instance identifier
	for _, member := range dbCluster.DBClusterMembers {
		if aws.ToBool(member.IsClusterWriter) {
			return aws.ToString(member.DBInstanceIdentifier), nil
		}
	}

	return "", fmt.Errorf("writer instance not found in cluster %s", d.ClusterID)
}

// GetWriterInstance retrieves the writer (primary) DB instance.
func (d *DocumentDB) GetWriterInstance(ctx context.Context) (*docdbTypes.DBInstance, error) {
	// Get all instances in the cluster
	describeInstancesInput := &docdb.DescribeDBInstancesInput{
		Filters: []docdbTypes.Filter{
			{
				Name:   aws.String("db-cluster-id"),
				Values: []string{d.ClusterID},
			},
		},
	}
	dbInstancesOutput, err := d.DocDBClient.DescribeDBInstances(ctx, describeInstancesInput)
	if err != nil {
		d.Logger.Error("Failed to describe DB instances", "Error", err)
		return nil, err
	}
	dbInstances := dbInstancesOutput.DBInstances

	// Get the writer instance identifier
	writerInstanceIdentifier, err := d.GetWriterInstanceIdentifier(ctx)
	if err != nil {
		return nil, err
	}

	for _, instance := range dbInstances {
		if aws.ToString(instance.DBInstanceIdentifier) == writerInstanceIdentifier {
			return &instance, nil
		}
	}

	return nil, fmt.Errorf("writer instance not found")
}

// HasAutoscalerTag checks if the instance has the autoscaler-created tag.
func (d *DocumentDB) HasAutoscalerTag(ctx context.Context, instance docdbTypes.DBInstance) (bool, error) {
	input := &docdb.ListTagsForResourceInput{
		ResourceName: instance.DBInstanceArn,
	}
	output, err := d.DocDBClient.ListTagsForResource(ctx, input)
	if err != nil {
		d.Logger.Error("Failed to list tags for resource", "Error", err, "ResourceName", aws.ToString(instance.DBInstanceArn))
		return false, err
	}
	for _, tag := range output.TagList {
		if aws.ToString(tag.Key) == "docdb-autoscaler-created" && aws.ToString(tag.Value) == "true" {
			return true, nil
		}
	}
	return false, nil
}

// AddReplicas adds the specified number of read replicas.
func (d *DocumentDB) AddReplicas(ctx context.Context, replicasToAdd int) error {
	writerInstance, err := d.GetWriterInstance(ctx)
	if err != nil {
		d.Logger.Error("Failed to get writer instance", "Error", err)
		return err
	}

	for i := 0; i < replicasToAdd; i++ {
		// Generate a shorter unique identifier
		timestamp := fmt.Sprintf("%d", time.Now().UnixNano())
		uniqueID := timestamp[len(timestamp)-9:] // Use last 9 digits to ensure uniqueness and keep length short

		baseIdentifier := fmt.Sprintf("%s-reader-%s", d.ClusterID, uniqueID)
		// Ensure the identifier is no more than 63 characters
		if len(baseIdentifier) > 63 {
			baseIdentifier = baseIdentifier[:63]
			// Ensure it doesn't end with a hyphen
			baseIdentifier = strings.TrimRight(baseIdentifier, "-")
		}

		// Ensure identifier starts with a letter and contains only allowed characters
		baseIdentifier = sanitizeDBInstanceIdentifier(baseIdentifier)

		// Determine the DBInstanceClass based on INSTANCE_TYPE environment variable
		var instanceClass *string
		if d.InstanceType != "" {
			instanceClass = aws.String(d.InstanceType)
		} else {
			instanceClass = writerInstance.DBInstanceClass
		}

		input := &docdb.CreateDBInstanceInput{
			DBClusterIdentifier:  aws.String(d.ClusterID),
			DBInstanceClass:      instanceClass,
			DBInstanceIdentifier: aws.String(baseIdentifier),
			Engine:               aws.String("docdb"), // Required field
			PromotionTier:        aws.Int32(15),       // Set PromotionTier to 15
		}

		if !d.DryRun {
			result, err := d.DocDBClient.CreateDBInstance(ctx, input)
			if err != nil {
				d.Logger.Error("Failed to add replicas", "Error", fmt.Sprintf("failed to create DB instance %s: %v", baseIdentifier, err), "ReplicasToAdd", replicasToAdd-i)
				return err
			}

			// Ensure result.DBInstance and result.DBInstance.DBInstanceArn are not nil
			if result.DBInstance == nil || result.DBInstance.DBInstanceArn == nil {
				d.Logger.Error("Failed to retrieve DBInstanceArn from CreateDBInstance response", "InstanceID", baseIdentifier)
				return fmt.Errorf("DBInstanceArn is nil for instance %s", baseIdentifier)
			}

			// Use the ARN from the CreateDBInstance response
			instanceArn := aws.ToString(result.DBInstance.DBInstanceArn)

			// Tag the new instance to indicate it was created by the autoscaler
			tagInput := &docdb.AddTagsToResourceInput{
				ResourceName: aws.String(instanceArn),
				Tags: []docdbTypes.Tag{
					{
						Key:   aws.String("docdb-autoscaler-created"),
						Value: aws.String("true"),
					},
				},
			}
			_, err = d.DocDBClient.AddTagsToResource(ctx, tagInput)
			if err != nil {
				d.Logger.Error("Failed to tag new read replica", "Error", err, "InstanceID", baseIdentifier)
				// Optionally handle this error
			}
			d.Logger.Info("Added read replica", "ClusterID", d.ClusterID, "InstanceID", baseIdentifier)
		} else {
			d.Logger.Info("[Dry Run] Would add read replica", "ClusterID", d.ClusterID, "InstanceID", baseIdentifier)
		}
	}

	return nil
}

// sanitizeDBInstanceIdentifier ensures the DBInstanceIdentifier complies with AWS constraints.
func sanitizeDBInstanceIdentifier(identifier string) string {
	// Ensure it starts with a letter
	if !isLetter(identifier[0]) {
		identifier = "a" + identifier
	}
	// Remove invalid characters
	validIdentifier := ""
	for _, ch := range identifier {
		if isValidDBInstanceIdentifierChar(ch) {
			validIdentifier += string(ch)
		} else {
			validIdentifier += "-"
		}
	}
	// Remove consecutive hyphens
	validIdentifier = strings.ReplaceAll(validIdentifier, "--", "-")
	// Trim any leading or trailing hyphens
	validIdentifier = strings.Trim(validIdentifier, "-")
	return validIdentifier
}

func isLetter(ch byte) bool {
	return (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z')
}

func isValidDBInstanceIdentifierChar(ch rune) bool {
	return (ch >= 'A' && ch <= 'Z') ||
		(ch >= 'a' && ch <= 'z') ||
		(ch >= '0' && ch <= '9') ||
		ch == '-'
}

// RemoveReplica removes a single read replica added by the autoscaler.
func (d *DocumentDB) RemoveReplica(ctx context.Context) error {
	// Get all instances in the cluster
	describeInstancesInput := &docdb.DescribeDBInstancesInput{
		Filters: []docdbTypes.Filter{
			{
				Name:   aws.String("db-cluster-id"),
				Values: []string{d.ClusterID},
			},
		},
	}
	dbInstancesOutput, err := d.DocDBClient.DescribeDBInstances(ctx, describeInstancesInput)
	if err != nil {
		d.Logger.Error("Failed to describe DB instances", "Error", err)
		return err
	}
	dbInstances := dbInstancesOutput.DBInstances

	// Get the writer instance identifier
	writerInstanceIdentifier, err := d.GetWriterInstanceIdentifier(ctx)
	if err != nil {
		d.Logger.Error("Failed to get writer instance identifier", "Error", err)
		return err
	}

	// Find instances to remove
	var instanceToRemove *docdbTypes.DBInstance
	for _, instance := range dbInstances {
		instanceID := aws.ToString(instance.DBInstanceIdentifier)
		if instanceID == writerInstanceIdentifier {
			continue // Skip the writer instance
		}

		// Check if the instance has the autoscaler tag
		hasTag, err := d.HasAutoscalerTag(ctx, instance)
		if err != nil {
			d.Logger.Error("Failed to check autoscaler tag", "Error", err, "InstanceID", instanceID)
			continue
		}

		// Check if the instance is in 'available' state
		if aws.ToString(instance.DBInstanceStatus) != "available" {
			d.Logger.Info("Instance is not in 'available' state, skipping", "InstanceID", instanceID, "Status", aws.ToString(instance.DBInstanceStatus))
			continue
		}

		if hasTag {
			// Found an instance to remove
			instanceToRemove = &instance
			break // Remove only one instance per invocation
		}
	}

	if instanceToRemove == nil {
		d.Logger.Info("No autoscaler-created instances found to remove")
		return nil // Nothing to remove
	}

	// Remove the instance
	if !d.DryRun {
		deleteInput := &docdb.DeleteDBInstanceInput{
			DBInstanceIdentifier: instanceToRemove.DBInstanceIdentifier,
		}
		_, err := d.DocDBClient.DeleteDBInstance(ctx, deleteInput)
		if err != nil {
			d.Logger.Error("Failed to delete read replica", "Error", err, "InstanceID", aws.ToString(instanceToRemove.DBInstanceIdentifier))
			return err
		}
		d.Logger.Info("Removed read replica", "ClusterID", d.ClusterID, "InstanceID", aws.ToString(instanceToRemove.DBInstanceIdentifier))
	} else {
		d.Logger.Info("[Dry Run] Would remove read replica", "ClusterID", d.ClusterID, "InstanceID", aws.ToString(instanceToRemove.DBInstanceIdentifier))
	}

	return nil
}

// ExecuteScalingAction performs the scaling logic.
func (d *DocumentDB) ExecuteScalingAction(ctx context.Context) error {
	if d.ScheduledScaling {
		// Use scheduled scaling logic
		return d.ExecuteScheduledScalingAction(ctx)
	} else {
		// Use existing metric-based scaling logic
		return d.ExecuteMetricBasedScalingAction(ctx)
	}
}

// ExecuteScheduledScalingAction handles the scheduled scaling logic.
func (d *DocumentDB) ExecuteScheduledScalingAction(ctx context.Context) error {
	d.Logger.Info("Executing scheduled scaling action", "ClusterID", d.ClusterID)

	// Get current reader instances
	readerInstances, err := d.GetReaderInstances(ctx)
	if err != nil {
		d.Logger.Error("Failed to retrieve reader instances", "Error", err)
		return err
	}

	// Count instances with the scheduler tag
	scheduledInstances := []docdbTypes.DBInstance{}
	for _, instance := range readerInstances {
		hasTag, err := d.HasSchedulerTag(ctx, instance)
		if err != nil {
			d.Logger.Error("Failed to check scheduler tag", "Error", err, "InstanceID", aws.ToString(instance.DBInstanceIdentifier))
			return err
		}
		if hasTag {
			scheduledInstances = append(scheduledInstances, instance)
		}
	}

	currentScheduledReplicas := len(scheduledInstances)
	d.Logger.Info("Current scheduled replicas", "Count", currentScheduledReplicas)

	// Determine action based on the presence of scheduled instances
	if currentScheduledReplicas > 0 {
		// Scale In: Remove all scheduled instances
		d.Logger.Info("Scaling In: Removing scheduled replicas", "ReplicasToRemove", currentScheduledReplicas)
		err := d.RemoveScheduledReplicas(ctx, scheduledInstances)
		if err != nil {
			d.Logger.Error("Failed to remove scheduled replicas", "Error", err)
			return err
		}
		// Send scale-in notification
		err = d.Notifier.SendScaleInNotification(d.ClusterID, currentScheduledReplicas)
		if err != nil {
			d.Logger.Error("Failed to send scale-in notification", "Error", err)
		}
	} else {
		// Scale Out: Add scheduled replicas
		replicasToAdd := d.ScheduleNumberReplicas
		desiredCapacity := len(readerInstances) + replicasToAdd

		// Enforce MAX_CAPACITY
		if desiredCapacity > d.MaxCapacity {
			replicasToAdd = d.MaxCapacity - len(readerInstances)
			if replicasToAdd <= 0 {
				d.Logger.Info("Desired capacity exceeds MAX_CAPACITY. No replicas to add.")
				return nil
			}
			d.Logger.Warn("Adjusting replicas to add due to MAX_CAPACITY constraint", "AdjustedReplicasToAdd", replicasToAdd)
		}

		// Enforce MIN_CAPACITY
		if desiredCapacity < d.MinCapacity {
			d.Logger.Info("Desired capacity is below MIN_CAPACITY. Adjusting to MIN_CAPACITY.", "MinCapacity", d.MinCapacity)
			replicasToAdd = d.MinCapacity - len(readerInstances)
		}

		d.Logger.Info("Scaling Out: Adding scheduled replicas", "ReplicasToAdd", replicasToAdd)
		err := d.AddScheduledReplicas(ctx, replicasToAdd)
		if err != nil {
			d.Logger.Error("Failed to add scheduled replicas", "Error", err)
			return err
		}
		// Send scale-out notification
		err = d.Notifier.SendScaleOutNotification(d.ClusterID, replicasToAdd)
		if err != nil {
			d.Logger.Error("Failed to send scale-out notification", "Error", err)
		}
	}

	return nil
}

// HasSchedulerTag checks if the instance has the scheduler tag.
func (d *DocumentDB) HasSchedulerTag(ctx context.Context, instance docdbTypes.DBInstance) (bool, error) {
	input := &docdb.ListTagsForResourceInput{
		ResourceName: instance.DBInstanceArn,
	}
	output, err := d.DocDBClient.ListTagsForResource(ctx, input)
	if err != nil {
		d.Logger.Error("Failed to list tags for resource", "Error", err, "ResourceName", aws.ToString(instance.DBInstanceArn))
		return false, err
	}
	for _, tag := range output.TagList {
		if aws.ToString(tag.Key) == "docdb-autoscaler-scheduler" && aws.ToString(tag.Value) == "true" {
			return true, nil
		}
	}
	return false, nil
}

// AddScheduledReplicas adds scheduled read replicas.
func (d *DocumentDB) AddScheduledReplicas(ctx context.Context, replicasToAdd int) error {
	var instanceClass *string

	if d.InstanceType != "" {
		instanceClass = aws.String(d.InstanceType)
	} else {
		writerInstance, err := d.GetWriterInstance(ctx)
		if err != nil {
			d.Logger.Error("Failed to get writer instance", "Error", err)
			return err
		}
		instanceClass = writerInstance.DBInstanceClass
	}

	for i := 0; i < replicasToAdd; i++ {
		// Generate a shorter unique identifier
		timestamp := fmt.Sprintf("%d", time.Now().UnixNano())
		uniqueID := timestamp[len(timestamp)-9:] // Use last 9 digits to ensure uniqueness and keep length short

		baseIdentifier := fmt.Sprintf("%s-scheduler-%s", d.ClusterID, uniqueID)
		// Ensure the identifier is no more than 63 characters
		if len(baseIdentifier) > 63 {
			baseIdentifier = baseIdentifier[:63]
			// Ensure it doesn't end with a hyphen
			baseIdentifier = strings.TrimRight(baseIdentifier, "-")
		}

		// Ensure identifier starts with a letter and contains only allowed characters
		baseIdentifier = sanitizeDBInstanceIdentifier(baseIdentifier)

		input := &docdb.CreateDBInstanceInput{
			DBClusterIdentifier:  aws.String(d.ClusterID),
			DBInstanceClass:      instanceClass,
			DBInstanceIdentifier: aws.String(baseIdentifier),
			Engine:               aws.String("docdb"), // Required field
			PromotionTier:        aws.Int32(15),       // Set PromotionTier to 15
		}

		if !d.DryRun {
			result, err := d.DocDBClient.CreateDBInstance(ctx, input)
			if err != nil {
				d.Logger.Error("Failed to create scheduled replica", "Error", fmt.Sprintf("failed to create DB instance %s: %v", baseIdentifier, err), "ReplicasToAdd", replicasToAdd-i)
				return err
			}

			// Ensure result.DBInstance and result.DBInstance.DBInstanceArn are not nil
			if result.DBInstance == nil || result.DBInstance.DBInstanceArn == nil {
				d.Logger.Error("Failed to retrieve DBInstanceArn from CreateDBInstance response", "InstanceID", baseIdentifier)
				return fmt.Errorf("DBInstanceArn is nil for instance %s", baseIdentifier)
			}

			// Use the ARN from the CreateDBInstance response
			instanceArn := aws.ToString(result.DBInstance.DBInstanceArn)

			// Tag the new instance to indicate it was created by the scheduler
			tagInput := &docdb.AddTagsToResourceInput{
				ResourceName: aws.String(instanceArn),
				Tags: []docdbTypes.Tag{
					{
						Key:   aws.String("docdb-autoscaler-scheduler"),
						Value: aws.String("true"),
					},
				},
			}
			_, err = d.DocDBClient.AddTagsToResource(ctx, tagInput)
			if err != nil {
				d.Logger.Error("Failed to tag new scheduled replica", "Error", err, "InstanceID", baseIdentifier)
				// Optionally handle this error
			}
			d.Logger.Info("Added scheduled read replica", "ClusterID", d.ClusterID, "InstanceID", baseIdentifier)
		} else {
			d.Logger.Info("[Dry Run] Would add scheduled read replica", "ClusterID", d.ClusterID, "InstanceID", baseIdentifier)
		}
	}

	return nil
}

// RemoveScheduledReplicas removes scheduled read replicas.
func (d *DocumentDB) RemoveScheduledReplicas(ctx context.Context, instances []docdbTypes.DBInstance) error {
	for _, instance := range instances {
		instanceID := aws.ToString(instance.DBInstanceIdentifier)

		// Check if the instance is in 'available' state
		if aws.ToString(instance.DBInstanceStatus) != "available" {
			d.Logger.Info("Instance is not in 'available' state, skipping", "InstanceID", instanceID, "Status", aws.ToString(instance.DBInstanceStatus))
			continue
		}

		// Remove the instance
		if !d.DryRun {
			deleteInput := &docdb.DeleteDBInstanceInput{
				DBInstanceIdentifier: instance.DBInstanceIdentifier,
			}
			_, err := d.DocDBClient.DeleteDBInstance(ctx, deleteInput)
			if err != nil {
				d.Logger.Error("Failed to delete scheduled read replica", "Error", err, "InstanceID", instanceID)
				return err
			}
			d.Logger.Info("Removed scheduled read replica", "ClusterID", d.ClusterID, "InstanceID", instanceID)
		} else {
			d.Logger.Info("[Dry Run] Would remove scheduled read replica", "ClusterID", d.ClusterID, "InstanceID", instanceID)
		}
	}
	return nil
}

// ExecuteMetricBasedScalingAction handles the existing metric-based scaling logic.
func (d *DocumentDB) ExecuteMetricBasedScalingAction(ctx context.Context) error {
	// For now, skipping the cooldown logic, currently implemented at EventBridge.

	// Step 1: Retrieve current metric value
	currentMetricValue, err := d.GetCurrentMetricValue(ctx)
	if err != nil {
		d.Logger.Error("Failed to retrieve current metric value", "Error", err)
		return err
	}
	d.Logger.Info("Retrieved current metric value", "MetricValue", currentMetricValue)

	// Step 2: Retrieve current capacity
	currentCapacity, err := d.GetCurrentCapacity(ctx)
	if err != nil {
		d.Logger.Error("Failed to retrieve current capacity", "Error", err)
		return err
	}
	d.Logger.Info("Retrieved current capacity", "CurrentCapacity", currentCapacity)

	// Step 3: Calculate desired capacity
	desiredCapacity := d.CalculateDesiredCapacity(currentMetricValue, currentCapacity)
	d.Logger.Info("Calculated desired capacity", "DesiredCapacity", desiredCapacity)

	// Step 4: Determine scaling action
	if desiredCapacity > currentCapacity {
		// Scale Out
		replicasToAdd := desiredCapacity - currentCapacity
		d.Logger.Info("Scaling Out", "ReplicasToAdd", replicasToAdd, "ClusterID", d.ClusterID)

		err := d.AddReplicas(ctx, replicasToAdd)
		if err != nil {
			d.Logger.Error("Failed to add replicas", "Error", err, "ReplicasToAdd", replicasToAdd)
			return err
		}
		// Send scale-out notification
		err = d.Notifier.SendScaleOutNotification(d.ClusterID, replicasToAdd)
		if err != nil {
			d.Logger.Error("Failed to send scale-out notification", "Error", err)
		}

	} else if desiredCapacity < currentCapacity {
		// Scale In
		replicasToRemove := 1 // Only remove one replica at a time
		d.Logger.Info("Scaling In", "ReplicasToRemove", replicasToRemove, "ClusterID", d.ClusterID)

		// Remove the required number of replicas (only 1)
		for i := 0; i < replicasToRemove; i++ {
			err := d.RemoveReplica(ctx)
			if err != nil {
				d.Logger.Error("Failed to remove replica", "Error", err, "Attempt", i+1)
				return err
			}
		}
		// Send scale-in notification
		err := d.Notifier.SendScaleInNotification(d.ClusterID, replicasToRemove)
		if err != nil {
			d.Logger.Error("Failed to send scale-in notification", "Error", err)
		}

	} else {
		// No action needed
		d.Logger.Info("No scaling action needed", "DesiredCapacity", desiredCapacity, "CurrentCapacity", currentCapacity, "ClusterID", d.ClusterID)
	}

	return nil
}
