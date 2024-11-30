# Example Usage

This just servers as an example. Feel free to add the autoscaling resources into your existing documentdb cluster module to make it more seamless to enable autoscaling.


```
### Example to enable autoscaling based on Metric ###
module "docdb_autoscaler" {
  source = "../../modules/aws/docdb-autoscaler"

  ## CW manager
  docdb_cluster_name        = "stagdocdb-transrep-cluster"
  cw_manager_image_uri      = "<imageTag>"
  metric_name               = "CPUUtilization"
  threshold_scale_out       = "50" # 50%
  threshold_scale_in        = "30" # 30%
  scale_out_cooldown_period = "rate(10 minutes)"
  scale_in_cooldown_period  = "rate(20 minutes)"

  ## DocDB Autoscaler
  min_capacity                    = 1
  max_capacity                    = 5
  target_value                    = 40   # 40%
  docdb_scale_out_cooldown_period = 600  # 10 minutes
  docdb_scale_in_cooldown_period  = 1200 # 20 minutes
  docdb_autoscaler_image_uri      = "<imageTag>"
  instance_type                   = "db.tg.medium" #OPTIONAL: to explictly choose the autoscale reader instance type

  tags = {
    ManagedBy = "Terraform"
  }
}

### Example to enable autoscaling based on schedule ###
module "docdb_autoscaler" {
  source = "../../modules/aws/docdb-autoscaler"

  ## DocDB Autoscaler
  docdb_cluster_name         = "stagdocdb-pfm-cluster"
  min_capacity               = 1
  max_capacity               = 5
  docdb_autoscaler_image_uri = "<tag>"
  scheduled_scaling          = true
  scheduled_scaling_state    = "ENABLED"
  schedule_number_replicas   = 4
  scale_out_schedule         = "cron(0 1 6 11 ? *)"  #9:00am MYT November 6
  scale_in_schedule          = "cron(0 11 6 11 ? *)" #7:00pm MYT November 6
  #instance_type = "db.t3.medium" #optional: to explictly choose the autoscale reader instance type

  tags = {
    ManagedBy = "Terraform"
  }
}
```