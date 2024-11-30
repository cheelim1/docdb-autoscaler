resource "aws_iam_role" "lambda_cw_manager_role" {
  count = var.scheduled_scaling ? 0 : 1

  name = "${var.docdb_cluster_name}-cw-manager"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action = "sts:AssumeRole"
      Effect = "Allow"
      Principal = {
        Service = "lambda.amazonaws.com"
      }
    }]
  })
}

# Attach policy to allow Lambda to interact with CloudWatch and EventBridge
resource "aws_iam_role_policy_attachment" "lambda_logs_policy" {
  count = var.scheduled_scaling ? 0 : 1

  role       = aws_iam_role.lambda_cw_manager_role[0].name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

resource "aws_iam_role_policy" "lambda_cw_manager_policy" {
  count = var.scheduled_scaling ? 0 : 1

  name = "${var.docdb_cluster_name}-cw-manager"
  role = aws_iam_role.lambda_cw_manager_role[0].id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Action = [
          "cloudwatch:SetAlarmState",
          "cloudwatch:DescribeAlarms"
        ]
        Effect   = "Allow"
        Resource = "*"
      }
    ]
  })
}

resource "aws_cloudwatch_metric_alarm" "docdb_cpu_alarm_scale_out" {
  count = var.scheduled_scaling ? 0 : 1

  alarm_name          = "${var.docdb_cluster_name}-scale-out"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 3 #minutes
  metric_name         = var.metric_name
  namespace           = "AWS/DocDB"
  period              = 60 #seconds
  statistic           = "Average"
  unit                = "Percent"
  threshold           = var.threshold_scale_out
  alarm_description   = "Alarm for ${var.metric_name} utilization above threshold"
  alarm_actions       = [aws_sns_topic.docdb_autoscaler_trigger_topic[0].arn]
  datapoints_to_alarm = var.datapoints_to_alarm_scale_out
  treat_missing_data  = "missing"

  dimensions = {
    DBClusterIdentifier = var.docdb_cluster_name
    Role                = "READER"
  }
}

resource "aws_cloudwatch_metric_alarm" "docdb_cpu_alarm_scale_in" {
  count = var.scheduled_scaling ? 0 : 1

  alarm_name          = "${var.docdb_cluster_name}-scale-in"
  comparison_operator = "LessThanThreshold"
  evaluation_periods  = 15 #minutes
  metric_name         = var.metric_name
  namespace           = "AWS/DocDB"
  period              = 60 #seconds
  statistic           = "Average"
  unit                = "Percent"
  threshold           = var.threshold_scale_in
  alarm_description   = "Alarm for ${var.metric_name} utilization below threshold"
  alarm_actions       = [aws_sns_topic.docdb_autoscaler_trigger_topic[0].arn]
  datapoints_to_alarm = var.datapoints_to_alarm_scale_in
  treat_missing_data  = "missing"

  dimensions = {
    DBClusterIdentifier = var.docdb_cluster_name
    Role                = "READER"
  }
}

resource "aws_lambda_function" "lambda_scale_out" {
  count = var.scheduled_scaling ? 0 : 1

  function_name = "${var.docdb_cluster_name}-scale-out"
  image_uri     = "${data.aws_caller_identity.current.account_id}.dkr.ecr.${var.aws_region}.amazonaws.com/${var.cw_manager_image_uri}"
  role          = aws_iam_role.lambda_cw_manager_role[0].arn
  package_type  = "Image"
  timeout       = 60

  environment {
    variables = {
      CLOUDWATCH_ALARM_NAME = "${var.docdb_cluster_name}-scale-out"
      ALARM_STATE           = "OK"
      ALARM_REASON          = "Scale out due to high ${var.metric_name} utilization"
    }
  }
}

resource "aws_lambda_function" "lambda_scale_in" {
  count = var.scheduled_scaling ? 0 : 1

  function_name = "${var.docdb_cluster_name}-scale-in"
  image_uri     = "${data.aws_caller_identity.current.account_id}.dkr.ecr.${var.aws_region}.amazonaws.com/${var.cw_manager_image_uri}"
  role          = aws_iam_role.lambda_cw_manager_role[0].arn
  package_type  = "Image"
  timeout       = 60

  environment {
    variables = {
      CLOUDWATCH_ALARM_NAME = "${var.docdb_cluster_name}-scale-in"
      ALARM_STATE           = "OK"
      ALARM_REASON          = "Scale in due to low ${var.metric_name} utilization"
    }
  }
}

# EventBridge Rule - Scale Out
resource "aws_cloudwatch_event_rule" "schedule_event_rule_scale_out" {
  count = var.scheduled_scaling ? 0 : 1

  name                = "${var.docdb_cluster_name}-cron-scale-out"
  description         = "Scheduled event to trigger Lambda scale out"
  schedule_expression = var.scale_out_cooldown_period
}

resource "aws_cloudwatch_event_target" "lambda_target_scale_out" {
  count = var.scheduled_scaling ? 0 : 1

  rule = aws_cloudwatch_event_rule.schedule_event_rule_scale_out[0].name
  arn  = aws_lambda_function.lambda_scale_out[0].arn
}

resource "aws_lambda_permission" "allow_eventbridge_to_invoke_lambda_scale_out" {
  count = var.scheduled_scaling ? 0 : 1

  statement_id  = "AllowEventBridgeInvokeLambdaScaleOut"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.lambda_scale_out[0].function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.schedule_event_rule_scale_out[0].arn
}

# EventBridge Rule - Scale In
resource "aws_cloudwatch_event_rule" "schedule_event_rule_scale_in" {
  count = var.scheduled_scaling ? 0 : 1

  name                = "${var.docdb_cluster_name}-cron-scale-in"
  description         = "Scheduled event to trigger Lambda scale in"
  schedule_expression = var.scale_in_cooldown_period
}

resource "aws_cloudwatch_event_target" "lambda_target_scale_in" {
  count = var.scheduled_scaling ? 0 : 1

  rule = aws_cloudwatch_event_rule.schedule_event_rule_scale_in[0].name
  arn  = aws_lambda_function.lambda_scale_in[0].arn
}

resource "aws_lambda_permission" "allow_eventbridge_to_invoke_lambda_scale_in" {
  count = var.scheduled_scaling ? 0 : 1

  statement_id  = "AllowEventBridgeInvokeLambdaScaleIn"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.lambda_scale_in[0].function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.schedule_event_rule_scale_in[0].arn
}
