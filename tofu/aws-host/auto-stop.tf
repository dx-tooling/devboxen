# Safety net against a forgotten running instance: every night at midnight (by default)
# EventBridge Scheduler stops it. It runs on AWS's side, so it works whatever state the
# host is in; on a stopped instance it does nothing. Stopping ends every session in the
# boxes (their data stays), so work past midnight needs `devboxen host start` again.
#
# Not taggable: the schedule itself (its group carries the tags) - it is here.

variable "auto_stop_cron" {
  description = "When the instance is stopped, in var.auto_stop_timezone. null = never."
  type        = string
  default     = "cron(0 0 * * ? *)" # 00:00 every day
  nullable    = true
}

variable "auto_stop_timezone" {
  description = "The time zone of auto_stop_cron (IANA, e.g. Europe/Berlin)."
  type        = string
  default     = "UTC"
}

resource "aws_scheduler_schedule_group" "devbox" {
  count = var.auto_stop_cron == null ? 0 : 1
  name  = local.name
}

data "aws_iam_policy_document" "scheduler_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["scheduler.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.this.account_id]
    }
  }
}

data "aws_caller_identity" "this" {}

data "aws_iam_policy_document" "stop_host" {
  statement {
    actions   = ["ec2:StopInstances"]
    resources = [aws_instance.host.arn]
  }
}

resource "aws_iam_role" "auto_stop" {
  count              = var.auto_stop_cron == null ? 0 : 1
  name               = "${local.name}-auto-stop"
  description        = "EventBridge Scheduler: stops the devbox host at midnight - ${local.repo_note}"
  assume_role_policy = data.aws_iam_policy_document.scheduler_assume.json
}

resource "aws_iam_role_policy" "auto_stop" {
  count  = var.auto_stop_cron == null ? 0 : 1
  name   = "stop-the-devbox-host"
  role   = aws_iam_role.auto_stop[0].id
  policy = data.aws_iam_policy_document.stop_host.json
}

resource "aws_scheduler_schedule" "auto_stop" {
  count       = var.auto_stop_cron == null ? 0 : 1
  name        = "${local.name}-auto-stop"
  group_name  = aws_scheduler_schedule_group.devbox[0].name
  description = "Stops the devbox host ${aws_instance.host.id} every night - ${local.repo_note}"

  schedule_expression          = var.auto_stop_cron
  schedule_expression_timezone = var.auto_stop_timezone

  flexible_time_window {
    mode = "OFF"
  }

  target {
    # EC2's StopInstances API, called directly (no Lambda in between).
    arn      = "arn:aws:scheduler:::aws-sdk:ec2:stopInstances"
    role_arn = aws_iam_role.auto_stop[0].arn
    input    = jsonencode({ InstanceIds = [aws_instance.host.id] })

    retry_policy {
      maximum_event_age_in_seconds = 3600 # a stop that is late by more than an hour is no use
      maximum_retry_attempts       = 10
    }
  }
}
