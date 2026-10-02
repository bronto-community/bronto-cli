# Searching: discover a field, aggregate by host, then cluster the errors.
Output search.webm
Output search.txt
Source settings.tape
# The -x scene is 24 rows tall; 580 px gives a 25-row terminal.
Set Height 580
Require bronto

Type "bronto fields -d prod/checkout-service status"
Sleep 300ms
Enter
Wait
Sleep 2s
Type `bronto search "status >= 500" -d prod/checkout-service --since 1h -g host --select "count(*)"`
Sleep 300ms
Enter
Wait
Sleep 3s

Hide
Ctrl+L
Sleep 200ms
Show

Type `bronto search "status >= 500" -d prod/checkout-service --since 1h --patterns`
Sleep 300ms
Enter
Wait
Sleep 2.5s
Type `bronto search "status >= 500" -d prod/checkout-service --since 1h -n 1 -x`
Sleep 300ms
Enter
Wait
Sleep 3.5s
Screenshot search.png
Sleep 500ms
