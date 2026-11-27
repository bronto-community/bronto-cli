# exported from provider integration 5f7b9d1e-3a4c-4d6f-9b8e-0a2c4e6f8b20
import {
  to = bronto_provider_integration.pagerduty
  id = "5f7b9d1e-3a4c-4d6f-9b8e-0a2c4e6f8b20"
}
resource "bronto_provider_integration" "pagerduty" {
  name = "PagerDuty"
  provider_type = "pagerduty"
  url = "https://example.com/pagerduty/checkout"
  auth_type = "api_key"
}
