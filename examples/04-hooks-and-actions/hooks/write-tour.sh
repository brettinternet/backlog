#!/bin/sh
# Ask an agent to write a tour of the change whose head is $1, then hand it to
# bl through the action's result.
set -eu
tour="$(mktemp -d)/change.tour"
claude -p "Write a CodeTour of commit $1 for backlog item $BL_ITEM_ID to $tour.
Run 'bl instructions tours' for the format and 'bl show $BL_ITEM_ID' for the
acceptance criteria to cite. Explain the change in reading order, one step per
idea." --allowedTools "Bash(bl instructions:*) Bash(bl show:*) Bash(git show:*) Bash(git diff:*) Read Write"
printf '{"tour":"%s"}\n' "$tour" > "$BL_RESULT"
