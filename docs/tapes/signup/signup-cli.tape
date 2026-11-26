# PROTOTYPE demo: a person signs up with `bronto signup`, clicks the link in
# the email (ada-clicks.sh plays that part), and the CLI finishes by itself.
# Rendered by docs/tapes/signup/render.sh against the fake sign-up backend.
Output signup-cli.mp4
Output signup-cli.gif
Output signup-cli.webm
Source settings.tape
Set Height 1000
Require bronto

Hide
Type "(ada-clicks &) ; clear"
Enter
Sleep 500ms
Show

Type "bronto signup"
Sleep 400ms
Enter
Sleep 1s
Type "ada@example.com"
Sleep 300ms
Enter
Sleep 500ms
Type "Ada"
Sleep 200ms
Enter
Sleep 400ms
Type "Lovelace"
Sleep 200ms
Enter
Sleep 400ms
Type "Analytical Engines"
Sleep 300ms
Enter
Sleep 1.2s
Type "eu"
Sleep 300ms
Enter
Sleep 1.2s
# The password prompts don't echo.
Type "Difference-Engine-1837!"
Sleep 300ms
Enter
Sleep 500ms
Type "Difference-Engine-1837!"
Sleep 300ms
Enter
Sleep 1.2s
Type "y"
Sleep 300ms
Enter

# The CLI waits; ada-clicks verifies after a few seconds; the CLI goes on.
Wait@40s
Sleep 5s

Type "bronto send -d hello -m 'hello world'"
Sleep 300ms
Enter
Wait
Sleep 1s
Type "bronto datasets list"
Sleep 300ms
Enter
Wait
Sleep 3.5s
