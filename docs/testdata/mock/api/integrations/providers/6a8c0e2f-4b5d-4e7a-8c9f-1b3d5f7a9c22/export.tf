resource "bronto_provider_integration" "rootly" {
  name          = "Rootly"
  provider_type = "rootly"
  url           = "https://example.com/rootly/payments"
  auth_type     = "hmac"
  # The secret is not exported. Set it by hand.
}
