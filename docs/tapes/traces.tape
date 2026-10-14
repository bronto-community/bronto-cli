# Traces: which service is slow, which spans fail, and one trace end to end.
Output traces.webm
Output traces.txt
Source settings.tape
Set Height 520
Require bronto

Type "bronto traces services --since 1h"
Sleep 300ms
Enter
Wait
Sleep 2s
Type "bronto traces list -s payments-api --errors -n 4 --since 1h --fields @time,operation,duration,status,trace_id"
Sleep 300ms
Enter
Wait
Sleep 3s

Hide
Ctrl+L
Sleep 200ms
Show

Type "bronto traces show 4bf92f3577b34da6a3ce929d0e0e4736"
Sleep 300ms
Enter
Wait
Sleep 4s
Screenshot traces.png
Sleep 500ms
