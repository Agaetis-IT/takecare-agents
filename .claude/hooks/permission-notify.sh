#!/bin/sh
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary
#
# Notification hook, matcher "permission_prompt": a macOS notification when a
# session waits on a permission. Reads the hook's JSON on stdin. Never blocks.
set -u
payload=$(cat)
msg=$(printf '%s' "$payload" | /usr/bin/python3 -I -c '
import json, sys
d = json.load(sys.stdin)
m = (d.get("message") or "A session waits on a permission").replace("\n", " ")[:200]
print(m.replace("\\", "\\\\").replace("\"", "\\\""))
' 2>/dev/null) || msg="A session waits on a permission"
project=$(basename "${CLAUDE_PROJECT_DIR:-$PWD}")
/usr/bin/osascript -e "display notification \"$msg\" with title \"Claude Code · $project\" sound name \"Glass\"" >/dev/null 2>&1
exit 0
