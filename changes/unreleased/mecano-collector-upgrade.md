Upgrade note

- The run-time read-only guard now also probes `get` on `nodes/proxy`, `nodes/stats` and `nodes/log` for every connector's ServiceAccount. A connector whose ServiceAccount holds one of these rights, through its own Role or any other binding, turns `ReadOnlyGuard` False after the upgrade. Remove the right before upgrading: the check is a tightening, never a weakening.
