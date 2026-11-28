#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
#
# Stub of hack/demo.sh for hack/bench-scenarios-test.sh: logs its call.
set -euo pipefail
echo "demo $*" >>"$STUB_LOG"
