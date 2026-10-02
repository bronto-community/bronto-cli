# Quickstart: find the datasets, pick one, and look at the last hour of errors.
Output quickstart.webm
Output quickstart.txt
Source settings.tape
Require bronto

Type "bronto datasets list"
Sleep 300ms
Enter
Wait
Sleep 2.5s

Hide
Ctrl+L
Sleep 200ms
Show

Type "bronto config set default_dataset prod/checkout-service"
Sleep 300ms
Enter
Wait
Sleep 1.5s
Type `bronto search "status >= 500" --since 1h --histogram --slices 10`
Sleep 300ms
Enter
Wait
Sleep 3s

Hide
Ctrl+L
Sleep 200ms
Show

Type `bronto search "status >= 500" --since 1h -n 3 --select @time --select host --select duration_ms`
Sleep 300ms
Enter
Wait
Sleep 3.5s
Screenshot quickstart.png
Sleep 500ms
