resource "bronto_provider_integration" "pagerduty" {
  name          = "PagerDuty"
  provider_type = "pagerduty"
  url           = "https://example.com/pagerduty/checkout"
  auth_type     = "api_key"
  # The secret is not exported. Set it by hand.
}
