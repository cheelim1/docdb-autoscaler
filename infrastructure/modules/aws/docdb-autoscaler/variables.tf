### CW ALARM MANAGER ###
variable "docdb_cluster_name" {
  description = "The name of the DocumentDB cluster"
  type        = string
}

variable "cw_manager_image_uri" {
  description = "The URI of the ECR repository containing the Lambda Docker image"
  type        = string
}

variable "metric_name" {
  description = "The name of the CloudWatch metric to monitor"
  type        = string
  default     = "CPUUtilization"
}

variable "threshold_scale_out" {
  description = "The Metric utilization threshold for the Scale Out CloudWatch alarm "
  type        = number
  default     = null
}

variable "threshold_scale_in" {
  description = "The Metric utilization threshold for the Scale In CloudWatch alarm"
  type        = number
  default     = null
}

variable "datapoints_to_alarm_scale_out" {
  description = "The number of datapoints to evaluate before triggering the alarm"
  type        = number
  default     = 3
}

variable "datapoints_to_alarm_scale_in" {
  description = "The number of datapoints to evaluate before triggering the alarm"
  type        = number
  default     = 15
}

variable "scale_out_cooldown_period" {
  description = "Cooldown period in seconds before allowing scale-out actions"
  type        = string
  default     = null
}

variable "scale_in_cooldown_period" {
  description = "Cooldown period in seconds before allowing scale-in actions"
  type        = string
  default     = null
}

### DOCDB AUTOSCALER ###
variable "aws_region" {
  description = "AWS region to deploy resources"
  type        = string
  default     = "us-east-1"
}

variable "tags" {
  description = "Tags to apply to all resources"
  type        = map(string)
}

variable "min_capacity" {
  description = "Minimum number of read replicas to maintain"
  type        = number
}

variable "max_capacity" {
  description = "Maximum number of read replicas allowed"
  type        = number
}

variable "target_value" {
  description = "Target value for the monitored metric to trigger scaling"
  type        = number
  default     = null
}

variable "instance_type" {
  description = "Instance type for new read replicas (e.g., r6g.large)"
  type        = string
  default     = null
}

variable "dryrun" {
  description = "Enable dry run mode for testing without making changes"
  type        = bool
  default     = false
}

variable "docdb_scale_out_cooldown_period" {
  description = "Cooldown period in seconds before allowing scale-out actions"
  type        = number
  default     = 600 #10 minutes
}
variable "docdb_scale_in_cooldown_period" {
  description = "Cooldown period in seconds before allowing scale-in actions"
  type        = number
  default     = 1200 #20 minutes
}

variable "max_retries" {
  description = "Maximum number of retry attempts for scaling actions"
  type        = number
  default     = 2
}

variable "initial_backoff" {
  description = "Initial backoff time in seconds before retrying scaling actions"
  type        = number
  default     = 1
}

variable "retry_delay_seconds" {
  description = "Initial delay in seconds before retrying scaling actions"
  type        = number
  default     = 2
}

variable "docdb_autoscaler_image_uri" {
  description = "The URI of the ECR repository containing the Lambda Docker image"
  type        = string
}

variable "email_notify" {
  description = "Email address to notify for errors"
  type        = string
  default     = null
}


### Scheduled Scaling ###
variable "scheduled_scaling" {
  description = "Enable or disable scheduled scaling."
  type        = bool
  default     = false
}

variable "scheduled_scaling_state" {
  description = "State of the scheduled scaling rule."
  type        = string
  default     = "DISABLED" #"ENABLED" 
}

variable "schedule_number_replicas" {
  description = "Number of replicas to add or remove during scheduled scaling."
  type        = number
  default     = null
}

variable "scale_out_schedule" {
  description = "Cron expression for scaling out (e.g., 'cron(0 9 * * ? *)' for 9 AM UTC daily)."
  type        = string
  default     = null #"cron(0 9 * * ? *)" # Example: 9 AM UTC daily
}

variable "scale_in_schedule" {
  description = "Cron expression for scaling in (e.g., 'cron(0 17 * * ? *)' for 5 PM UTC daily)."
  type        = string
  default     = null #"cron(0 17 * * ? *)" # Example: 5 PM UTC daily
}
