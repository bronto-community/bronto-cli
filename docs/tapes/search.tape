# Searching: discover a field, aggregate by host, then cluster the errors.
Output search.webm
Output search.txt
Source settings.tape
# The -x scene picks its fields so its height doesn't depend on the data:
# live events carry more metadata than the mock's, and an unbounded -x
# scrolled off screen when recorded against the CI account. 480 px gives a
# 20-row terminal for the 16-row scene.
Set Height 480
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
Type `bronto search "status >= 500" -d prod/checkout-service --since 1h -n 1 -x --fields @time,host,status,path,duration_ms,trace_id,message`
Sleep 300ms
Enter
Wait
Sleep 3.5s
Screenshot search.png
Sleep 500ms
