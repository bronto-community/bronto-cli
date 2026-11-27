# exported from provider integration 6a8c0e2f-4b5d-4e7a-8c9f-1b3d5f7a9c22
import {
  to = bronto_provider_integration.rootly
  id = "6a8c0e2f-4b5d-4e7a-8c9f-1b3d5f7a9c22"
}
resource "bronto_provider_integration" "rootly" {
  name = "Rootly"
  provider_type = "rootly"
  url = "https://webhooks.rootly.com/webhooks/incoming/payments"
  auth_type = "hmac"
}
