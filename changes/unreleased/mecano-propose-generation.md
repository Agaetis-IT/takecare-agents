Fixed

- The Mécano's propose route now fails closed while its argument rule changes (spec 12 §6.4): the gateway's verdict counts only when it is on the rule rendered now, at the policy's current generation, so a change of `maintenance.repositories` closes the route and takes `propose_change` from the agent until the gateway has read the new rule. The Intendant's metrics route reads its rule's verdict the same way. A suspended Mécano's propose route admits nothing.
- The read-only guard also checks that no connector's ServiceAccount may `create` on `nodes/proxy`, a write through the kubelet.
