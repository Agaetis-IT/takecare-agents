# Spike 001 — kagent vs HolmesGPT for the Doc. Throwaway harness.
# Every target uses spikes/001/out/kubeconfig, never the current kube context.
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

.PHONY: help up down status scenario-% reset

help: ## list targets
	@grep -E '^[a-zA-Z_%-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

up: ## create the cluster and install the whole stack (re-runnable)
	scripts/up.sh all

status: ## show what is deployed
	scripts/up.sh status

scenario-%: ## start a scenario: proxy-timeout | bad-config-rollout | missing-secret-key
	scripts/scenario.sh start $*

reset: ## put every scenario back to healthy
	scripts/scenario.sh reset

down: ## delete the k3d cluster
	k3d cluster delete spike-001
