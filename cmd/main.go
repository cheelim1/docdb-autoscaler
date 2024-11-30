package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/docdb"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/cheelim1/docdb-autoscaler/pkg/autoscaling"
	"github.com/cheelim1/docdb-autoscaler/pkg/logger"
	"github.com/cheelim1/docdb-autoscaler/pkg/notifications"
)

// ScalingMessage defines the structure of the scaling parameters sent via SNS or EventBridge
type ScalingMessage struct {
	ScalingType    string `json:"ScalingType"`
	NumberReplicas int    `json:"NumberReplicas"`
}

func main() {
	lambda.Start(handler)
}

func handler(ctx context.Context, event json.RawMessage) error {
	// Initialize logger
	loggerInstance := logger.NewLogger()
	loggerInstance.Info("Lambda function invoked")

	// Attempt to parse as SNSEvent
	var snsEvent events.SNSEvent
	if err := json.Unmarshal(event, &snsEvent); err == nil && len(snsEvent.Records) > 0 {
		loggerInstance.Info("Detected SNSEvent")
		return handleSNSEvent(ctx, loggerInstance, snsEvent)
	}

	// Attempt to parse as CloudWatchEvent
	var cwEvent events.CloudWatchEvent
	if err := json.Unmarshal(event, &cwEvent); err == nil && cwEvent.Source != "" {
		loggerInstance.Info("Detected CloudWatchEvent")
		return handleCloudWatchEvent(ctx, loggerInstance, cwEvent)
	}

	// If neither, log unsupported event type
	loggerInstance.Warn("Received unsupported event type", "EventType", fmt.Sprintf("%T", event), "EventData", string(event))
	return nil
}

func handleSNSEvent(ctx context.Context, loggerInstance *slog.Logger, snsEvent events.SNSEvent) error {
	// Load AWS configuration
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		loggerInstance.Error("Failed to load AWS configuration", "Error", err)
		return err
	}

	// Initialize AWS clients
	docdbClient := docdb.NewFromConfig(cfg)
	cloudwatchClient := cloudwatch.NewFromConfig(cfg)
	snsClient := sns.NewFromConfig(cfg)
	rdsClient := rds.NewFromConfig(cfg)

	// Initialize notifier
	snsTopicArn := os.Getenv("SNS_TOPIC_ARN")
	if snsTopicArn == "" {
		loggerInstance.Error("Environment variable SNS_TOPIC_ARN is not set")
		return fmt.Errorf("SNS_TOPIC_ARN is not set")
	}
	notifier := notifications.NewNotifier(snsClient, snsTopicArn)

	// Read common environment variables
	clusterID := os.Getenv("CLUSTER_IDENTIFIER")
	if clusterID == "" {
		loggerInstance.Error("Environment variable CLUSTER_IDENTIFIER is not set")
		return fmt.Errorf("CLUSTER_IDENTIFIER is not set")
	}

	minCapacityStr := os.Getenv("MIN_CAPACITY")
	if minCapacityStr == "" {
		loggerInstance.Error("Environment variable MIN_CAPACITY is not set")
		return fmt.Errorf("MIN_CAPACITY is not set")
	}
	minCapacity, err := strconv.Atoi(minCapacityStr)
	if err != nil {
		loggerInstance.Error("Invalid MIN_CAPACITY", "Error", err)
		return err
	}

	maxCapacityStr := os.Getenv("MAX_CAPACITY")
	if maxCapacityStr == "" {
		loggerInstance.Error("Environment variable MAX_CAPACITY is not set")
		return fmt.Errorf("MAX_CAPACITY is not set")
	}
	maxCapacity, err := strconv.Atoi(maxCapacityStr)
	if err != nil {
		loggerInstance.Error("Invalid MAX_CAPACITY", "Error", err)
		return err
	}

	// Read Scaling Type
	scheduledScalingStr := os.Getenv("SCHEDULED_SCALING")
	scheduledScaling := false
	if scheduledScalingStr != "" {
		scheduledScaling, err = strconv.ParseBool(scheduledScalingStr)
		if err != nil {
			loggerInstance.Error("Invalid SCHEDULED_SCALING value", "Error", err)
			return err
		}
	}

	// Initialize variables for scaling type-specific environment variables
	var (
		metricName             string
		targetValue            float64
		scaleInCooldown        int
		scaleOutCooldown       int
		scheduleNumberReplicas int
	)

	if scheduledScaling {
		// Scheduled Scaling: Read relevant environment variables
		scheduleNumberReplicasStr := os.Getenv("SCHEDULE_NUMBER_REPLICAS")
		if scheduleNumberReplicasStr == "" {
			loggerInstance.Error("Environment variable SCHEDULE_NUMBER_REPLICAS is not set")
			return fmt.Errorf("SCHEDULE_NUMBER_REPLICAS is not set")
		}
		scheduleNumberReplicas, err = strconv.Atoi(scheduleNumberReplicasStr)
		if err != nil {
			loggerInstance.Error("Invalid SCHEDULE_NUMBER_REPLICAS", "Error", err)
			return err
		}
	} else {
		// Metric-Based Scaling: Read relevant environment variables
		metricName = os.Getenv("METRIC_NAME")
		if metricName == "" {
			loggerInstance.Error("Environment variable METRIC_NAME is not set")
			return fmt.Errorf("METRIC_NAME is not set")
		}

		targetValueStr := os.Getenv("TARGET_VALUE")
		if targetValueStr == "" {
			loggerInstance.Error("Environment variable TARGET_VALUE is not set")
			return fmt.Errorf("TARGET_VALUE is not set")
		}
		targetValue, err = strconv.ParseFloat(targetValueStr, 64)
		if err != nil {
			loggerInstance.Error("Invalid TARGET_VALUE", "Error", err)
			return err
		}

		scaleInCooldownStr := os.Getenv("SCALE_IN_COOLDOWN")
		if scaleInCooldownStr == "" {
			loggerInstance.Error("Environment variable SCALE_IN_COOLDOWN is not set")
			return fmt.Errorf("SCALE_IN_COOLDOWN is not set")
		}
		scaleInCooldown, err = strconv.Atoi(scaleInCooldownStr)
		if err != nil {
			loggerInstance.Error("Invalid SCALE_IN_COOLDOWN", "Error", err)
			return err
		}

		scaleOutCooldownStr := os.Getenv("SCALE_OUT_COOLDOWN")
		if scaleOutCooldownStr == "" {
			loggerInstance.Error("Environment variable SCALE_OUT_COOLDOWN is not set")
			return fmt.Errorf("SCALE_OUT_COOLDOWN is not set")
		}
		scaleOutCooldown, err = strconv.Atoi(scaleOutCooldownStr)
		if err != nil {
			loggerInstance.Error("Invalid SCALE_OUT_COOLDOWN", "Error", err)
			return err
		}
	}

	// Read Retry Configuration environment variables
	maxRetriesStr := os.Getenv("MAX_RETRIES")
	maxRetries := 5 // Default value
	if maxRetriesStr != "" {
		maxRetries, err = strconv.Atoi(maxRetriesStr)
		if err != nil {
			loggerInstance.Error("Invalid MAX_RETRIES value", "Error", err)
			return err
		}
	}

	initialBackoffStr := os.Getenv("INITIAL_BACKOFF")
	initialBackoff := time.Second // Default 1 second
	if initialBackoffStr != "" {
		initialBackoffSeconds, err := strconv.Atoi(initialBackoffStr)
		if err != nil {
			loggerInstance.Error("Invalid INITIAL_BACKOFF value", "Error", err)
			return err
		}
		initialBackoff = time.Duration(initialBackoffSeconds) * time.Second
	}

	// Read DRYRUN flag
	dryRunStr := os.Getenv("DRYRUN")
	dryRun := false
	if dryRunStr != "" {
		dryRun, err = strconv.ParseBool(dryRunStr)
		if err != nil {
			loggerInstance.Error("Invalid DRYRUN value", "Error", err)
			return err
		}
	}

	// Read INSTANCE_TYPE as optional
	instanceType := os.Getenv("INSTANCE_TYPE")
	if instanceType == "" {
		loggerInstance.Info("INSTANCE_TYPE not set. Will use writer instance's type for scaling.")
	} else {
		loggerInstance.Info("INSTANCE_TYPE set", "InstanceType", instanceType)
	}

	// Initialize the DocumentDB autoscaler with the RDS client
	docdbAutoscaler := autoscaling.NewDocumentDB(
		clusterID,
		minCapacity,
		maxCapacity,
		metricName,
		targetValue,
		scaleInCooldown,
		scaleOutCooldown,
		instanceType,
		dryRun,
		scheduledScaling,
		scheduleNumberReplicas,
		docdbClient,
		cloudwatchClient,
		notifier,
		loggerInstance,
		rdsClient,
	)

	// Initialize aggregation variables for dry-run
	var totalDryRunAdditions int
	var totalDryRunRemovals int

	// Process each SNS record
	for _, record := range snsEvent.Records {
		snsRecord := record.SNS
		loggerInstance.Info("Received SNS message", "MessageID", snsRecord.MessageID, "Subject", snsRecord.Subject)

		// Proceed with scaling logic
		additions, removals, err := processScaling(ctx, loggerInstance, docdbAutoscaler, snsRecord.Message, maxRetries, initialBackoff)
		if err != nil {
			loggerInstance.Error("Scaling process failed", "Error", err)
			return err
		}

		// Aggregate dry-run actions
		if docdbAutoscaler.DryRun {
			totalDryRunAdditions += additions
			totalDryRunRemovals += removals
		}
	}

	// If dry-run, log the aggregated summary
	if docdbAutoscaler.DryRun {
		loggerInstance.Info("Dry Run Summary",
			"TotalReplicasToAdd", totalDryRunAdditions,
			"TotalReplicasToRemove", totalDryRunRemovals,
		)
	}

	return nil
}

func handleCloudWatchEvent(ctx context.Context, loggerInstance *slog.Logger, cwEvent events.CloudWatchEvent) error {
	// Load AWS configuration
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		loggerInstance.Error("Failed to load AWS configuration", "Error", err)
		return err
	}

	// Initialize AWS clients
	docdbClient := docdb.NewFromConfig(cfg)
	cloudwatchClient := cloudwatch.NewFromConfig(cfg)
	snsClient := sns.NewFromConfig(cfg)
	rdsClient := rds.NewFromConfig(cfg)

	// Initialize notifier
	snsTopicArn := os.Getenv("SNS_TOPIC_ARN")
	if snsTopicArn == "" {
		loggerInstance.Error("Environment variable SNS_TOPIC_ARN is not set")
		return fmt.Errorf("SNS_TOPIC_ARN is not set")
	}
	notifier := notifications.NewNotifier(snsClient, snsTopicArn)

	// Read common environment variables
	clusterID := os.Getenv("CLUSTER_IDENTIFIER")
	if clusterID == "" {
		loggerInstance.Error("Environment variable CLUSTER_IDENTIFIER is not set")
		return fmt.Errorf("CLUSTER_IDENTIFIER is not set")
	}

	minCapacityStr := os.Getenv("MIN_CAPACITY")
	if minCapacityStr == "" {
		loggerInstance.Error("Environment variable MIN_CAPACITY is not set")
		return fmt.Errorf("MIN_CAPACITY is not set")
	}
	minCapacity, err := strconv.Atoi(minCapacityStr)
	if err != nil {
		loggerInstance.Error("Invalid MIN_CAPACITY", "Error", err)
		return err
	}

	maxCapacityStr := os.Getenv("MAX_CAPACITY")
	if maxCapacityStr == "" {
		loggerInstance.Error("Environment variable MAX_CAPACITY is not set")
		return fmt.Errorf("MAX_CAPACITY is not set")
	}
	maxCapacity, err := strconv.Atoi(maxCapacityStr)
	if err != nil {
		loggerInstance.Error("Invalid MAX_CAPACITY", "Error", err)
		return err
	}

	// Read Scaling Type
	scheduledScalingStr := os.Getenv("SCHEDULED_SCALING")
	scheduledScaling := false
	if scheduledScalingStr != "" {
		scheduledScaling, err = strconv.ParseBool(scheduledScalingStr)
		if err != nil {
			loggerInstance.Error("Invalid SCHEDULED_SCALING value", "Error", err)
			return err
		}
	}

	// Initialize variables for scaling type-specific environment variables
	var (
		metricName             string
		targetValue            float64
		scaleInCooldown        int
		scaleOutCooldown       int
		scheduleNumberReplicas int
	)

	if scheduledScaling {
		// Scheduled Scaling: Read relevant environment variables
		scheduleNumberReplicasStr := os.Getenv("SCHEDULE_NUMBER_REPLICAS")
		if scheduleNumberReplicasStr == "" {
			loggerInstance.Error("Environment variable SCHEDULE_NUMBER_REPLICAS is not set")
			return fmt.Errorf("SCHEDULE_NUMBER_REPLICAS is not set")
		}
		scheduleNumberReplicas, err = strconv.Atoi(scheduleNumberReplicasStr)
		if err != nil {
			loggerInstance.Error("Invalid SCHEDULE_NUMBER_REPLICAS", "Error", err)
			return err
		}
	} else {
		// Metric-Based Scaling: Read relevant environment variables
		metricName = os.Getenv("METRIC_NAME")
		if metricName == "" {
			loggerInstance.Error("Environment variable METRIC_NAME is not set")
			return fmt.Errorf("METRIC_NAME is not set")
		}

		targetValueStr := os.Getenv("TARGET_VALUE")
		if targetValueStr == "" {
			loggerInstance.Error("Environment variable TARGET_VALUE is not set")
			return fmt.Errorf("TARGET_VALUE is not set")
		}
		targetValue, err = strconv.ParseFloat(targetValueStr, 64)
		if err != nil {
			loggerInstance.Error("Invalid TARGET_VALUE", "Error", err)
			return err
		}

		scaleInCooldownStr := os.Getenv("SCALE_IN_COOLDOWN")
		if scaleInCooldownStr == "" {
			loggerInstance.Error("Environment variable SCALE_IN_COOLDOWN is not set")
			return fmt.Errorf("SCALE_IN_COOLDOWN is not set")
		}
		scaleInCooldown, err = strconv.Atoi(scaleInCooldownStr)
		if err != nil {
			loggerInstance.Error("Invalid SCALE_IN_COOLDOWN", "Error", err)
			return err
		}

		scaleOutCooldownStr := os.Getenv("SCALE_OUT_COOLDOWN")
		if scaleOutCooldownStr == "" {
			loggerInstance.Error("Environment variable SCALE_OUT_COOLDOWN is not set")
			return fmt.Errorf("SCALE_OUT_COOLDOWN is not set")
		}
		scaleOutCooldown, err = strconv.Atoi(scaleOutCooldownStr)
		if err != nil {
			loggerInstance.Error("Invalid SCALE_OUT_COOLDOWN", "Error", err)
			return err
		}
	}

	// Read Retry Configuration environment variables
	maxRetriesStr := os.Getenv("MAX_RETRIES")
	maxRetries := 5 // Default value
	if maxRetriesStr != "" {
		maxRetries, err = strconv.Atoi(maxRetriesStr)
		if err != nil {
			loggerInstance.Error("Invalid MAX_RETRIES value", "Error", err)
			return err
		}
	}

	initialBackoffStr := os.Getenv("INITIAL_BACKOFF")
	initialBackoff := time.Second // Default 1 second
	if initialBackoffStr != "" {
		initialBackoffSeconds, err := strconv.Atoi(initialBackoffStr)
		if err != nil {
			loggerInstance.Error("Invalid INITIAL_BACKOFF value", "Error", err)
			return err
		}
		initialBackoff = time.Duration(initialBackoffSeconds) * time.Second
	}

	// Read DRYRUN flag
	dryRunStr := os.Getenv("DRYRUN")
	dryRun := false
	if dryRunStr != "" {
		dryRun, err = strconv.ParseBool(dryRunStr)
		if err != nil {
			loggerInstance.Error("Invalid DRYRUN value", "Error", err)
			return err
		}
	}

	// Read INSTANCE_TYPE as optional
	instanceType := os.Getenv("INSTANCE_TYPE")
	if instanceType == "" {
		loggerInstance.Info("INSTANCE_TYPE not set. Will use writer instance's type for scaling.")
	} else {
		loggerInstance.Info("INSTANCE_TYPE set", "InstanceType", instanceType)
	}

	// Initialize the DocumentDB autoscaler with the RDS client
	docdbAutoscaler := autoscaling.NewDocumentDB(
		clusterID,
		minCapacity,
		maxCapacity,
		metricName,
		targetValue,
		scaleInCooldown,
		scaleOutCooldown,
		instanceType,
		dryRun,
		scheduledScaling,
		scheduleNumberReplicas,
		docdbClient,
		cloudwatchClient,
		notifier,
		loggerInstance,
		rdsClient,
	)

	// Initialize aggregation variables for dry-run
	var totalDryRunAdditions int
	var totalDryRunRemovals int

	// Execute scaling action
	additions, removals, err := processScaling(ctx, loggerInstance, docdbAutoscaler, "", maxRetries, initialBackoff)
	if err != nil {
		loggerInstance.Error("Scheduled scaling action failed", "Error", err)
		return err
	}

	// Aggregate dry-run actions
	if docdbAutoscaler.DryRun {
		totalDryRunAdditions += additions
		totalDryRunRemovals += removals
	}

	// If dry-run, log the aggregated summary
	if docdbAutoscaler.DryRun {
		loggerInstance.Info("Dry Run Summary",
			"TotalReplicasToAdd", totalDryRunAdditions,
			"TotalReplicasToRemove", totalDryRunRemovals,
		)
	} else {
		loggerInstance.Info("Scheduled scaling action executed successfully")
	}

	return nil
}

// processScaling handles the scaling logic for both SNS-based and scheduled scaling
// Returns the number of replicas to add and remove for aggregation
func processScaling(ctx context.Context, loggerInstance *slog.Logger, autoscaler *autoscaling.DocumentDB, snsMessage string, maxRetries int, initialBackoff time.Duration) (int, int, error) {
	var replicasToAdd int
	var replicasToRemove int

	if snsMessage != "" {
		// Metric-Based Scaling
		var scalingMessage ScalingMessage
		err := json.Unmarshal([]byte(snsMessage), &scalingMessage)
		if err != nil {
			loggerInstance.Error("Failed to parse scaling message", "Error", err)
			return 0, 0, err
		}

		loggerInstance.Info("Parsed Scaling Message from SNS", "ScalingType", scalingMessage.ScalingType, "NumberReplicas", scalingMessage.NumberReplicas)

		// Update autoscaler settings based on SNS message
		autoscaler.ScheduledScaling = false // Metric-based scaling
		autoscaler.ScheduleNumberReplicas = scalingMessage.NumberReplicas
	} else {
		// Scheduled Scaling
		loggerInstance.Info("Executing Scheduled Scaling", "NumberReplicas", autoscaler.ScheduleNumberReplicas)
	}

	// Determine desired action based on scaling settings
	if autoscaler.ScheduleNumberReplicas > 0 {
		replicasToAdd = autoscaler.ScheduleNumberReplicas
	} else if autoscaler.ScheduleNumberReplicas < 0 {
		replicasToRemove = int(math.Abs(float64(autoscaler.ScheduleNumberReplicas)))
	}

	// Execute scaling action with retry logic
	err := executeWithRetry(ctx, loggerInstance, autoscaler.ExecuteScalingAction, maxRetries, initialBackoff)
	if err != nil {
		loggerInstance.Error("Scaling action failed after retries", "Error", err)
		return replicasToAdd, replicasToRemove, err
	}

	// Determine if additions or removals were performed
	if replicasToAdd > 0 {
		if autoscaler.DryRun {
			loggerInstance.Info("Dry Run: Would add read replicas", "ReplicasToAdd", replicasToAdd)
		}
	}
	if replicasToRemove > 0 {
		if autoscaler.DryRun {
			loggerInstance.Info("Dry Run: Would remove read replicas", "ReplicasToRemove", replicasToRemove)
		}
	}

	return replicasToAdd, replicasToRemove, nil
}

// executeWithRetry attempts to execute the provided action with exponential backoff retries
func executeWithRetry(ctx context.Context, loggerInstance *slog.Logger, action func(context.Context) error, maxRetries int, initialBackoff time.Duration) error {
	backoff := initialBackoff

	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := action(ctx)
		if err == nil {
			return nil
		}

		loggerInstance.Warn("Scaling action failed, retrying...", "Attempt", attempt, "Error", err)

		// Wait before the next retry
		time.Sleep(backoff)

		// Exponential backoff with a maximum cap (e.g., 32 seconds)
		backoff = backoff * 2
		if backoff > 32*time.Second {
			backoff = 32 * time.Second
		}
	}

	return fmt.Errorf("scaling action failed after %d attempts", maxRetries)
}
