# Searching: discover a field, aggregate by host, then cluster the errors.
Output search.webm
Output search.txt
Source settings.tape
# The -x scene picks its fields: live events carry more metadata than the
# mock's, and a bare -x scrolled off screen when recorded against the CI
# account. --fields fixes the field count, not the height (-x never
# truncates values), so the chosen fields are short ones. The scene is 16
# rows; 480 px (20 rows) leaves headroom over the default 17.
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
