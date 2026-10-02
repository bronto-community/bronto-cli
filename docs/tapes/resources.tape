# Resources: monitors, a safe dry run, and who gets paged.
Output resources.webm
Output resources.txt
Source settings.tape
# The dry-run scene is 18 rows tall (the mute plan prints as indented JSON);
# 480 px gives a 20-row terminal.
Set Height 480
Require bronto

Type "bronto monitors list"
Sleep 300ms
Enter
Wait
Sleep 2s
Type `bronto monitors events "High 5xx rate on checkout" --fields monitor_status,message`
Sleep 300ms
Enter
Wait
Sleep 3s

Hide
Ctrl+L
Sleep 200ms
Show

Type `bronto monitors mute "High 5xx rate on checkout" --dry-run`
Sleep 300ms
Enter
Wait
Sleep 2s
Type "bronto groups members oncall --fields member_id,member_type"
Sleep 300ms
Enter
Wait
Sleep 1.5s
Type "bronto users list"
Sleep 300ms
Enter
Wait
Sleep 3s
Screenshot resources.png
Sleep 500ms
