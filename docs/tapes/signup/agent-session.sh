#!/usr/bin/env bash
# agent-session renders a MOCK-UP of a coding-agent session that signs a
# user up for Bronto, for signup-agent.tape. Scripted: the user's lines, the
# agent's prose and the mail tool call. Real: every bronto command and its
# output (run here against the fake sign-up backend), and the email, which
# is fetched from the fake backend's inbox.
set -euo pipefail

email=ada@example.com
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

dim=$'\033[2m' bold=$'\033[1m' off=$'\033[0m'
green=$'\033[32m' cyan=$'\033[36m' magenta=$'\033[35m'

# A fixed note on the first row; everything else scrolls below it.
rows=$(tput lines)
printf '\033[2J\033[H\033[30;43m MOCK-UP of an agent session. The bronto commands and their output are real (fake backend). \033[0m\n'
printf '\033[3;%dr\033[3;1H' "$rows"

user() { # typed character by character
  printf '\n%s> %s' "$cyan$bold" "$off$bold"
  local i
  for ((i = 0; i < ${#1}; i++)); do
    printf '%s' "${1:i:1}"
    sleep 0.03
  done
  printf '%s\n' "$off"
  sleep 0.8
}
say() { # agent prose: first line gets the bullet
  printf '\n● %s\n' "$1"
  shift
  for l in "$@"; do printf '  %s\n' "$l"; done
}
tool() { # a tool call header: colour, name, then the argument lines
  local colour=$1 name=$2
  shift 2
  printf '\n%s●%s %s%s%s(%s' "$colour" "$off" "$bold" "$name" "$off" "$1"
  shift
  for l in "$@"; do printf '\n    %s' "$l"; done
  printf ')\n'
  sleep 0.9
}
result() { # stdin: a tool's output, shown under the call (outer blank lines dropped)
  local first=1 line
  awk 'NF { for (i = 0; i < b; i++) print ""; b = 0; print; s = 1; next } s { b++ }' | while IFS= read -r line; do
    if [ "$first" = 1 ]; then printf '  %s⎿%s  %s\n' "$dim" "$off" "$line"; else printf '     %s\n' "$line"; fi
    first=0
  done
}

user "Sign me up for Bronto and set up the CLI. Use my work email $email."
sleep 0.6
say "I can do that with \`bronto signup\`. Two choices are yours to make first:" \
  "1. Region, eu or us? It is permanent for the organization." \
  "2. Do you accept the terms of service? https://www.bronto.io/legal/terms"
sleep 4.5
user "eu. Yes, I accept. Call the org Analytical Engines."
sleep 0.5

tool "$green" Bash "bronto signup --email $email --first-name Ada --last-name Lovelace \\" \
  "--org 'Analytical Engines' --region eu --accept-terms --generate-password" \
  "${dim}run in background${off}"
bronto signup --email "$email" --first-name Ada --last-name Lovelace \
  --org 'Analytical Engines' --region eu --accept-terms --generate-password \
  </dev/null >"$work/signup.out" 2>&1 &
signup_pid=$!
for _ in $(seq 1 100); do grep -q 'No browser at hand' "$work/signup.out" && break; sleep 0.1; done
shown=$(wc -l <"$work/signup.out")
result <"$work/signup.out"
sleep 7

say "The account exists and \`bronto signup\` is waiting for the email verification." \
  "I'll find the email and verify from here."
sleep 3
tool "$magenta" mail.search 'from: "bronto", newer_than: "5m"'
mail=$(curl -fsS "$BRONTO_BASE_URL/__signup/inbox?email=$email")
link=$(jq -r '.[0].link' <<<"$mail")
jq -r '"1 message", "From:    " + .[0].from, "Subject: " + .[0].subject, "", .[0].body' <<<"$mail" | result
sleep 5

tool "$green" Bash "bronto signup confirm \\" "'$link'"
bronto signup confirm "$link" 2>&1 | result
sleep 3.5

wait "$signup_pid"
printf '\n%s●%s %sBackground command finished%s: bronto signup (exit 0)\n' "$green" "$off" "$bold" "$off"
tail -n +"$((shown + 1))" "$work/signup.out" | result
sleep 7

tool "$green" Bash "bronto send -d hello -m 'hello world' && bronto datasets list --jq .dataset"
{ bronto send -d hello -m 'hello world' && bronto datasets list --jq .dataset; } 2>&1 | result
sleep 3

say "Done. Your Bronto account ($email, organization Analytical Engines, region eu) exists." \
  "The CLI has its own API key, and a first test event went into the dataset \"hello\"." \
  "${bold}The account password was generated and shown once, above: save it in your password manager now.${off}"
sleep 7
printf '\033[r\033[%d;1H' "$rows"
