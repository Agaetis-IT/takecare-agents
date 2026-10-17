# Spike 001 — kagent vs HolmesGPT for the Doc. Throwaway harness.
# Every target uses spikes/001/out/kubeconfig, never the current kube context.
SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

SCENARIO ?= proxy-timeout
ENGINE   ?= kagent
N        ?= 1

.PHONY: help up down status scenario-% reset run screens

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

run: ## run N investigations: make run ENGINE=kagent|holmes-native|holmes-mcp SCENARIO=... N=5
	scripts/run.sh $(ENGINE) $(SCENARIO) $(N)

screens: ## port-forward Jaeger, Prometheus, Alertmanager
	scripts/screens.sh

down: ## delete the k3d cluster
	k3d cluster delete spike-001
