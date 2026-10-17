# Vendored ACK CRD

`rds.services.k8s.aws_dbproxies.yaml` is the **real** CRD of AWS Controllers for
Kubernetes, copied verbatim from the `aws-controllers-k8s/rds-controller`
repository, tag **v1.12.0**:

    config/crd/bases/rds.services.k8s.aws_dbproxies.yaml

Provenance: copied unchanged from the challenge POC
(`sre-agents/demo/claude/poc/content/ack/`), which vendored it from that tag.
It is vendored rather than downloaded so that the demo works offline, and
pinned to a tag so that it does not change between two rehearsals.

**Caveat to state as is**: on k3d there is no AWS, so the CRD is installed
without its controller. The `DBProxy` object, its schema and its
`idleClientTimeout` field are real; only reconciliation is missing, so
`status` stays empty.
