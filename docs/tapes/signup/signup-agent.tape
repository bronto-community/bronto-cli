# PROTOTYPE demo: the same sign-up driven by a coding agent that can read
# the user's mail. The session is a scripted MOCK-UP (agent-session.sh);
# the bronto commands in it are real and run against the fake backend.
Output signup-agent.mp4
Output signup-agent.gif
Output signup-agent.webm
Source settings.tape
Set Height 1000
Require bronto

Hide
Type "clear; agent-session"
Enter
Sleep 300ms
Show
# The session draws "> " for the user's lines, so wait for the shell prompt
# only once its last line is on screen.
Wait+Screen@120s /in your password manager/
Sleep 6s
