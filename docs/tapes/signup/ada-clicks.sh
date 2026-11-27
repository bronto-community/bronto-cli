#!/usr/bin/env bash
# ada-clicks plays the person clicking the link in the verification email,
# for signup-cli.tape: it waits until the fake inbox has the email, shows a
# caption on the terminal, then verifies with the real 'bronto signup
# confirm'. Runs in the background of the recording shell.
set -euo pipefail
email=${1:-ada@example.com}
link=""
for _ in $(seq 1 600); do
  link=$(curl -fsS "$BRONTO_BASE_URL/__signup/inbox?email=$email" | jq -r '.[0].link // empty')
  [ -n "$link" ] && break
  sleep 0.2
done
[ -n "$link" ] || exit 1
sleep 5
# \r + erase-line replaces the CLI's progress line; it redraws on its next poll.
printf '\r\033[K\033[1;33m# (Ada clicks the link in her inbox)\033[0m\n'
sleep 1.5
bronto signup confirm "$link" --quiet
