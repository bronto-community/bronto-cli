# exported from slack integration 3d5f7b9c-1e2a-4b4d-9f6c-8e0a2c4d6f18
import {
  to = bronto_slack_integration.checkout_alerts
  id = "3d5f7b9c-1e2a-4b4d-9f6c-8e0a2c4d6f18"
}
resource "bronto_slack_integration" "checkout_alerts" {
  name = "checkout-alerts"
  workspace_id = "2c4e6a8b-0d1f-4a3c-8e5b-7d9f1b3c5e17"
  channels = [
    { name = "checkout-alerts", include_graph_image = true },
    { name = "oncall", include_graph_image = false },
  ]
}
