# A module: the root module that uses it configures the provider (region, credentials,
# allowed account, default tags) and the state backend.
terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}
