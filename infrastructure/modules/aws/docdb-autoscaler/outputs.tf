output "lambda_function_name" {
  description = "Name of the Lambda function for docdb-autoscaler"
  value       = aws_lambda_function.docdb_autoscaler_lambda.function_name
}

output "lambda_function_arn" {
  description = "ARN of the Lambda function for docdb-autoscaler"
  value       = aws_lambda_function.docdb_autoscaler_lambda.arn
}
