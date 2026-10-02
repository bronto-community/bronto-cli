# Tail: follow checkout-service live as the payments-api timeouts start.
# The mock reveals 3 new events per poll and stops after 12, so the final
# screen is always the same (see internal/tools/docsmock/mock/tail.go).
Output tail.webm
Output tail.txt
Source settings.tape
Require bronto

Type "bronto tail -d prod/checkout-service --interval 1s --fields @time,@status,host,message"
Sleep 300ms
Enter
Sleep 7s
Screenshot tail.png
Sleep 300ms
Ctrl+C
Wait
Sleep 1.5s
