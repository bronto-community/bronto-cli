# Scripting: JSON when piped, --jq, and exit codes you can branch on.
Output scripting.webm
Output scripting.txt
Source settings.tape
Require bronto
Require jq

Type `bronto search "status >= 500" -d prod/checkout-service --since 1h -o json | jq length`
Sleep 300ms
Enter
Wait
Sleep 2s
Type `bronto search "status >= 500" -d prod/checkout-service --select host -o jsonl | jq -r .host | sort | uniq -c | sed 's/^ *//'`
Sleep 300ms
Enter
Wait
Sleep 2s
Type `bronto monitors list -o json --jq 'select(.status == "ALERT") | .name'`
Sleep 300ms
Enter
Wait
Sleep 2s

Hide
Ctrl+L
Sleep 200ms
Show

Type `bronto search "status >= 500" -d checkout-service; echo "exit code: $?"`
Sleep 300ms
Enter
Wait
Sleep 3s
Screenshot scripting.png
Sleep 500ms
