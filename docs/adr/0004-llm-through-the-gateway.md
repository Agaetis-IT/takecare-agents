# ADR 0004 — LLM: OpenAI-compatible, always through the gateway

| | |
|---|---|
| Status | Accepted |
| Date | 2026-10-16 |

## Context

The promise is "the model of your choice, on your infrastructure". The POC calls OpenRouter, either directly or through agentgateway (`ROUTE_HOLMES_VIA_GATEWAY`). The POC's cost measurements (`poc.sh kpis`) rely on the gateway.

## Decision

1. **Single interface**: an OpenAI-compatible API (chat completions). This covers OpenRouter, OpenAI, Azure OpenAI, Mistral, and vLLM or Ollama on the customer's side.
2. **Always through agentgateway.** No engine (kagent, ADR 0007) calls the provider directly. The operator configures the engines to target the gateway with a dummy key. The real key only exists in a Secret read by the gateway.
3. **Declared in the `TakeCarePlatform`**:

   ```yaml
   spec:
     models:
       - name: default
         provider: openai-compatible
         baseURL: https://openrouter.ai/api/v1
         model: <model identifier>
         apiKeySecretRef: { name: takecare-model, key: apiKey }
         pricing: { inputPerMTok: 0.0, cachedInputPerMTok: 0.0, outputPerMTok: 0.0, currency: USD }  # for the estimated cost
   ```

   A role can target another model of the list (`spec.model` in `RoleCommonSpec`).
4. **Demo**: OpenRouter, with the key passed to `make demo-up` through an environment variable, never written to a file tracked by Git.
5. **CI**: a fake OpenAI-compatible provider that replays recorded responses, for deterministic and free end-to-end tests. The bench with a real model runs on demand (`make demo-bench`).

## Consequences

- Cost, tokens and traces of each investigation are measured in one place, whatever the engine (see ADR 0006).
- The Ranger (milestone 2) will build on the same gateway: masking, allowed models, caps.
- A customer with sovereignty requirements plugs in their vLLM by changing `baseURL`, nothing else.

## Points of attention

- Models differ in how they handle tool calls. The accuracy bench must run on every model announced as compatible.
- Prices are for estimation, not billing.
