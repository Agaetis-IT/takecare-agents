// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package semconv holds every telemetry attribute name TakeCare emits
// (ADR 0006): the OpenTelemetry GenAI semantic conventions it follows, and
// its own takecare.* namespace. No other package spells these names; a test
// enforces it. Moving to a newer convention version is a deliberate change
// here, noted in the CHANGELOG.
//
// Pinned conventions: open-telemetry/semantic-conventions-genai, the names of
// the GenAI registry at semantic-conventions v1.37.0 (status: Development).
// Note for the Doc (PR 4): agentgateway 1.6.0 still labels the provider
// gen_ai.system on its spans and metrics where these conventions say
// gen_ai.provider.name; correlating the operator's spans with the gateway's
// must read both.
package semconv

// GenAI attributes, as in the conventions.
const (
	GenAIOperationName  = "gen_ai.operation.name"
	GenAIAgentName      = "gen_ai.agent.name"
	GenAIAgentID        = "gen_ai.agent.id"
	GenAIConversationID = "gen_ai.conversation.id"
	GenAIProviderName   = "gen_ai.provider.name"
	GenAIRequestModel   = "gen_ai.request.model"
	GenAIToolName       = "gen_ai.tool.name"
	GenAIToolCallID     = "gen_ai.tool.call.id"
	GenAIUsageInput     = "gen_ai.usage.input_tokens"
	GenAIUsageOutput    = "gen_ai.usage.output_tokens"

	// GenAISystemLegacy is what agentgateway 1.6.0 emits for the provider.
	GenAISystemLegacy = "gen_ai.system"
)

// Operation names.
const (
	OperationInvokeAgent = "invoke_agent"
	OperationChat        = "chat"
	OperationExecuteTool = "execute_tool"
)

// TakeCare attributes (ADR 0006, spec 02).
const (
	Role             = "takecare.role"
	Level            = "takecare.level"
	AlertName        = "takecare.alert.name"
	AlertFingerprint = "takecare.alert.fingerprint"
	Scenario         = "takecare.scenario"

	// RunOutcome is how the engine's run ended (completed, failed,
	// failed_after_answer, lost, timed_out), on the root span.
	RunOutcome = "takecare.run.outcome"
	// RunAttempt counts the starts of a run: a lost task is retried once.
	RunAttempt = "takecare.run.attempt"
	// DegradedConnectors and DegradedReason say which sources a run went
	// without (issue #10: the run degrades and says so, in the trace too).
	DegradedConnectors = "takecare.degraded.connectors"
	DegradedReason     = "takecare.degraded.reason"

	// Journal stamps the gateway's access log lines the Ranger's journal
	// keeps (spec 09 §7).
	Journal = "takecare.journal"

	// A survey of the Intendant (spec 10 §10): its record and trigger, on
	// the root span; a ground listed (its connector, cluster and namespace
	// count) or the Map read (its entry count) on their spans; a layer's
	// counts on that layer's span.
	SurveyName       = "takecare.survey.name"
	SurveyTrigger    = "takecare.survey.trigger"
	SurveyConnector  = "takecare.survey.connector"
	SurveyCluster    = "takecare.survey.cluster"
	SurveyNamespaces = "takecare.survey.namespaces"
	SurveyEntries    = "takecare.survey.entries"
	SurveyLayer      = "takecare.survey.layer"
	SurveySeen       = "takecare.survey.seen"
	SurveyMatched    = "takecare.survey.matched"
	SurveyProposed   = "takecare.survey.proposed"
	SurveyNotSeen    = "takecare.survey.not_seen"
	SurveyProposals  = "takecare.survey.proposals"
)

// Span events of an investigation (spec 02).
const (
	EventAlertAccepted          = "alert.accepted"
	EventInvestigationStarted   = "investigation.started"
	EventInvestigationCompleted = "investigation.completed"
	// EventInvestigationRetried: a lost task was started again.
	EventInvestigationRetried = "investigation.retried"
	// EventInvestigationResumed: the operator restarted during the run and
	// follows the task again under the same trace; the first root span is
	// lost with the process that held it.
	EventInvestigationResumed = "investigation.resumed"
	EventDiagnosisPublished   = "diagnosis.published"
)

// ServiceName is the operator's OpenTelemetry resource service.name.
const ServiceName = "takecare-operator"
