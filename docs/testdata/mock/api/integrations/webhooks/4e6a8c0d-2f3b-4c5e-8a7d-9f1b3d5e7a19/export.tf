resource "bronto_webhook_integration" "oncall_pager" {
  name = "oncall-pager"
  url  = "https://example.com/hooks/oncall"
}
