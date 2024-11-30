data "aws_caller_identity" "current" {}

resource "aws_iam_role" "lambda_docdb_autoscaler_role" {
  name = "${var.docdb_cluster_name}-docdb-autoscaler"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Principal = {
          Service = "lambda.amazonaws.com"
        }
        Action = "sts:AssumeRole"
      }
    ]
  })
  tags = var.tags
}

resource "aws_iam_role_policy" "lambda_policy" {
  name = "${var.docdb_cluster_name}-docdb-autoscaler"
  role = aws_iam_role.lambda_docdb_autoscaler_role.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = [
          "rds:DescribeDBInstances",
          "rds:ListTagsForResource",
          "rds:DescribeDBClusters",
          "rds:AddTagsToResource"
        ]
        Resource = "*"
      },
      {
        Effect = "Allow"
        Action = [
          "rds:CreateDBInstance",
          "rds:DeleteDBInstance",
        ]
        Resource = [ ## Restrict to only delete/create the instances managed by the autoscaler
          "arn:aws:rds:${var.aws_region}:${data.aws_caller_identity.current.account_id}:db:${var.docdb_cluster_name}-*",
          "arn:aws:rds:${var.aws_region}:${data.aws_caller_identity.current.account_id}:cluster:${var.docdb_cluster_name}"
        ]
      },
      {
        Effect = "Allow"
        Action = [
          "cloudwatch:GetMetricStatistics"
        ]
        Resource = "*"
      },
      {
        Effect = "Allow"
        Action = [
          "sns:Publish"
        ]
        Resource = [
          aws_sns_topic.docdb_autoscaler_notification_topic.arn
        ]
      },
      {
        Effect = "Allow"
        Action = [
          "logs:CreateLogGroup",
          "logs:CreateLogStream",
          "logs:PutLogEvents"
        ]
        Resource = "arn:aws:logs:${var.aws_region}:${data.aws_caller_identity.current.account_id}:log-group:/aws/lambda/*"
      }
    ]
  })
}

# Notification SNS Topic (for Lambda to send notifications)
resource "aws_sns_topic" "docdb_autoscaler_notification_topic" {
  name = "${var.docdb_cluster_name}-docdb-autoscaler-notify"
  tags = var.tags
}

# Trigger SNS Topic (for CloudWatch Alarms to trigger Lambda)
resource "aws_sns_topic" "docdb_autoscaler_trigger_topic" {
  count = var.scheduled_scaling ? 0 : 1
  name  = "${var.docdb_cluster_name}-docdb-autoscaler-trigger"
  tags  = var.tags
}

resource "aws_sns_topic_policy" "docdb_autoscaler_trigger_topic_policy" {
  count = var.scheduled_scaling ? 0 : 1
  arn   = aws_sns_topic.docdb_autoscaler_trigger_topic[0].arn

  policy = jsonencode({
    Version = "2012-10-17",
    Statement : [
      {
        Sid : "AllowCloudWatchPublish",
        Effect : "Allow",
        Principal : { Service : "cloudwatch.amazonaws.com" },
        Action : "SNS:Publish",
        Resource : "${aws_sns_topic.docdb_autoscaler_trigger_topic[0].arn}"
      }
    ]
  })
}

resource "aws_lambda_function" "docdb_autoscaler_lambda" {
  function_name = "${var.docdb_cluster_name}-docdb-autoscaler"

  package_type = "Image"
  image_uri    = "ghcr.io/cheelim1/docdb-autoscaler/image:${var.docdb_autoscaler_image_uri}"
  role         = aws_iam_role.lambda_docdb_autoscaler_role.arn

  environment {
    variables = {
      CLUSTER_IDENTIFIER       = var.docdb_cluster_name
      MIN_CAPACITY             = tostring(var.min_capacity)
      MAX_CAPACITY             = tostring(var.max_capacity)
      METRIC_NAME              = var.metric_name
      TARGET_VALUE             = tostring(var.target_value)
      SCALE_IN_COOLDOWN        = tostring(var.docdb_scale_in_cooldown_period)
      SCALE_OUT_COOLDOWN       = tostring(var.docdb_scale_out_cooldown_period)
      INSTANCE_TYPE            = var.instance_type
      DRYRUN                   = tostring(var.dryrun)
      SNS_TOPIC_ARN            = aws_sns_topic.docdb_autoscaler_notification_topic.arn
      MAX_RETRIES              = tostring(var.max_retries)         # Optional: For retry logic
      INITIAL_BACKOFF          = tostring(var.initial_backoff)     # Optional: For retry delay
      RETRY_DELAY_SECONDS      = tostring(var.retry_delay_seconds) # Optional: For retry delay
      SCHEDULED_SCALING        = tostring(var.scheduled_scaling)
      SCHEDULE_NUMBER_REPLICAS = tostring(var.schedule_number_replicas)
    }
  }

  timeout     = 60
  memory_size = 256
  tags        = var.tags
}

# Lambda Permission to Allow SNS to Invoke It
resource "aws_lambda_permission" "allow_sns" {
  count = var.scheduled_scaling ? 0 : 1

  statement_id  = "AllowExecutionFromSNS"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.docdb_autoscaler_lambda.function_name
  principal     = "sns.amazonaws.com"
  source_arn    = aws_sns_topic.docdb_autoscaler_trigger_topic[0].arn
}

# SNS Subscription to Invoke Lambda
resource "aws_sns_topic_subscription" "lambda_subscription" {
  count     = var.scheduled_scaling ? 0 : 1
  topic_arn = aws_sns_topic.docdb_autoscaler_trigger_topic[0].arn
  protocol  = "lambda"
  endpoint  = aws_lambda_function.docdb_autoscaler_lambda.arn
}

# Limit Lambda concurrency to 1 (To prevent racecondition)
resource "aws_lambda_function_event_invoke_config" "docdb_autoscaler_invocation" {
  function_name = aws_lambda_function.docdb_autoscaler_lambda.function_name

  maximum_retry_attempts       = 0
  maximum_event_age_in_seconds = 60
}

##### Scheduled Scaling #####
resource "aws_cloudwatch_event_rule" "scheduled_docdb_scale_out_rule" {
  count = var.scheduled_scaling ? 1 : 0

  name                = "${var.docdb_cluster_name}-scale-out-rule"
  description         = "Scheduled rule to scale out DocumentDB cluster."
  schedule_expression = var.scale_out_schedule
  state               = var.scheduled_scaling_state
}

resource "aws_cloudwatch_event_target" "scale_out_target" {
  count = var.scheduled_scaling ? 1 : 0
  rule  = aws_cloudwatch_event_rule.scheduled_docdb_scale_out_rule[0].name
  arn   = aws_lambda_function.docdb_autoscaler_lambda.arn
  # role_arn = aws_iam_role.lambda_docdb_autoscaler_role.arn
}

resource "aws_cloudwatch_event_rule" "scheduled_docdb_scale_in_rule" {
  count               = var.scheduled_scaling ? 1 : 0
  name                = "${var.docdb_cluster_name}-scale-in-rule"
  description         = "Scheduled rule to scale in DocumentDB cluster."
  schedule_expression = var.scale_in_schedule
  state               = var.scheduled_scaling_state
}

resource "aws_cloudwatch_event_target" "scale_in_target" {
  count = var.scheduled_scaling ? 1 : 0

  rule = aws_cloudwatch_event_rule.scheduled_docdb_scale_in_rule[0].name
  arn  = aws_lambda_function.docdb_autoscaler_lambda.arn
  # role_arn = aws_iam_role.lambda_docdb_autoscaler_role.arn
}

resource "aws_sns_topic" "docdb_autoscaler_scheduled_trigger_topic" {
  count = var.scheduled_scaling ? 1 : 0
  name  = "${var.docdb_cluster_name}-docdb-autoscaler-scheduled-trigger"
  tags  = var.tags
}

resource "aws_lambda_permission" "allow_scale_out_eventbridge_to_invoke_lambda_docdb_autoscaler" {
  count = var.scheduled_scaling ? 1 : 0

  statement_id  = "AllowScaleOutEventBridgeInvokeLambdaScaleDocDBAutoscaler"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.docdb_autoscaler_lambda.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.scheduled_docdb_scale_out_rule[0].arn
}

resource "aws_lambda_permission" "allow_scale_in_eventbridge_to_invoke_lambda_docdb_autoscaler" {
  count = var.scheduled_scaling ? 1 : 0

  statement_id  = "AllowScaleInEventBridgeInvokeLambdaDocDBAutoscaler"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.docdb_autoscaler_lambda.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.scheduled_docdb_scale_in_rule[0].arn
}

