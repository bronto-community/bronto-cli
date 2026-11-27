# exported from webhook integration 4e6a8c0d-2f3b-4c5e-8a7d-9f1b3d5e7a19
import {
  to = bronto_webhook_integration.oncall_pager
  id = "4e6a8c0d-2f3b-4c5e-8a7d-9f1b3d5e7a19"
}
resource "bronto_webhook_integration" "oncall_pager" {
  name = "oncall-pager"
  url = "https://example.com/hooks/oncall"
}
