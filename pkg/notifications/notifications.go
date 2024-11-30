package notifications

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/sns"
)

// SNSAPI defines the interface for Amazon SNS interactions.
type SNSAPI interface {
	Publish(ctx context.Context, params *sns.PublishInput, optFns ...func(*sns.Options)) (*sns.PublishOutput, error)
}

// NotifierInterface defines the methods that our notifier should implement.
// This allows us to use different implementations, such as a NoOpNotifier in tests.
type NotifierInterface interface {
	SendScaleOutNotification(clusterID string, replicasAdded int) error
	SendScaleInNotification(clusterID string, replicasRemoved int) error
	SendFailureNotification(clusterID, errorMessage, action string) error
}

// Notifier is responsible for sending notifications using SNS.
type Notifier struct {
	SNSClient SNSAPI
	TopicARN  string
	Subject   string
}

// NewNotifier creates a new Notifier instance.
func NewNotifier(snsClient SNSAPI, topicARN string) *Notifier {
	return &Notifier{
		SNSClient: snsClient,
		TopicARN:  topicARN,
		Subject:   "DocumentDB Autoscaler Notification",
	}
}

// Ensure Notifier implements NotifierInterface
var _ NotifierInterface = (*Notifier)(nil)

// SendScaleOutNotification sends a notification when scaling out.
func (n *Notifier) SendScaleOutNotification(clusterID string, replicasAdded int) error {
	message := fmt.Sprintf("Scaled out cluster %s by adding %d replicas.", clusterID, replicasAdded)
	return n.publish(message)
}

// SendScaleInNotification sends a notification when scaling in.
func (n *Notifier) SendScaleInNotification(clusterID string, replicasRemoved int) error {
	message := fmt.Sprintf("Scaled in cluster %s by removing %d replicas.", clusterID, replicasRemoved)
	return n.publish(message)
}

// SendFailureNotification sends a notification when a scaling action fails.
func (n *Notifier) SendFailureNotification(clusterID, errorMessage, action string) error {
	message := fmt.Sprintf("Failed to %s on cluster %s: %s", action, clusterID, errorMessage)
	return n.publish(message)
}

// publish sends a message to the SNS topic.
func (n *Notifier) publish(message string) error {
	input := &sns.PublishInput{
		Message:  &message,
		TopicArn: &n.TopicARN,
		Subject:  &n.Subject,
	}
	_, err := n.SNSClient.Publish(context.Background(), input)
	return err
}
