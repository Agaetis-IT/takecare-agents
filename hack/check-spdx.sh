#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary
#
# Fails if one of our source files lacks an SPDX license identifier in its
# first lines (docs/licensing.md). Code copied from a third party keeps its
# own SPDX identifier; it must still have one.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

missing=0
while IFS= read -r -d '' file; do
  if ! head -n 5 "$file" | grep -q 'SPDX-License-Identifier:'; then
    echo "missing SPDX header: $file"
    missing=1
  fi
done < <(git ls-files -z --cached --others --exclude-standard \
  -- '*.go' '*.py' '*.sh' 'Makefile' '**/Dockerfile' 'Dockerfile' ':!:spikes/**')

if [ "$missing" -ne 0 ]; then
  echo "add the header described in docs/licensing.md"
  exit 1
fi
