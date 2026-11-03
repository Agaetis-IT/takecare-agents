// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package engine is the boundary between TakeCare and its agent engine
// (ADR 0007). The interfaces are defined here, where the controllers consume
// them; the implementation for the pinned engine lives in a subpackage, and
// nothing outside that subpackage names an engine type.
package engine

import (
	"context"
	"errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Model is what the engine needs to reach one model through the gateway
// (ADR 0004): never a provider key, only the gateway route and a Secret
// holding a dummy key the engine's runtime insists on.
type Model struct {
	// Name of the model entry in the platform; names the engine's object.
	Name string
	// Namespace of the engine, where the object is written.
	Namespace string
	// Model identifier the provider expects, passed through the gateway.
	Model string
	// GatewayURL is the OpenAI-compatible base URL of the gateway, with its
	// /v1 path.
	GatewayURL string
	// DummyKeySecret names the Secret, in Namespace, whose DummyKeyField holds
	// the dummy key.
	DummyKeySecret string
	DummyKeyField  string
	// Owner is set on the engine's object so that it follows the platform.
	Owner metav1.OwnerReference
	// Labels copied onto the engine's object.
	Labels map[string]string
}

// Readiness is the engine's verdict on one of its objects.
type Readiness struct {
	Ready   bool
	Reason  string
	Message string
}

// Models provisions the engine's model configuration. Implementations write
// their own objects and report on them; the platform controller never sees
// an engine type.
type Models interface {
	// EnsureModel creates or updates the engine's configuration for a model.
	EnsureModel(ctx context.Context, m Model) error
	// ModelReadiness reports whether the engine accepted a model.
	ModelReadiness(ctx context.Context, namespace, name string) (Readiness, error)
	// OwnedTypes lists the kinds EnsureModel writes, for the controller to watch.
	OwnedTypes() []schema.GroupVersionKind
}

// Agent is a role rendered for the engine: what the engine needs to run the
// role's agent, and nothing about what triggers its runs (spec 06 §2: the
// engine sees a run, never an alert).
type Agent struct {
	// Name of the agent in the engine; names the engine's objects.
	Name string
	// Namespace of the engine, where the objects are written.
	Namespace string
	// Description shown by the engine.
	Description string
	// Instructions are the agent's system message.
	Instructions string
	// Model names the engine's model configuration (engine.Model.Name).
	Model string
	// Tools are the agent's MCP servers, each behind the role's own gateway
	// route, each with its allow-list.
	Tools []ToolServer
	// Skill is what the agent's A2A card advertises.
	Skill Skill
	// CaptureContent lets the engine record prompts, answers and tool
	// payloads in its spans (ADR 0006); off unless the platform turns it on.
	CaptureContent bool
	// Labels copied onto every object, among them the owner's: objects in
	// the engine namespace cannot carry an owner reference to the role.
	Labels map[string]string
}

// ToolServer is one connector as the agent sees it.
type ToolServer struct {
	// Connector names the connector, for the object's name and description.
	Connector string
	// URL of the role's MCP route on the gateway.
	URL string
	// Tools is the allow-list. It is never empty: an empty list means every
	// tool of the server to some engines, so a server without allowed tools
	// is not given to the agent at all.
	Tools []string
}

// Skill is one A2A skill.
type Skill struct {
	ID          string
	Description string
	Tags        []string
}

// Endpoint is where the gateway reaches an agent's A2A server.
type Endpoint struct {
	// Host and Port of the engine's A2A server.
	Host string
	Port int64
	// Path is the agent's A2A path on that server, without a trailing
	// slash. The gateway's route is a prefix on it: the agent's path and
	// what the engine serves below it, never anything above it (ADR 0009:
	// the engine's API is never exposed). Each implementation records what
	// its engine serves below that path.
	Path string
}

// Agents provisions and reports on the engine's agents.
type Agents interface {
	// EnsureAgent creates or updates the agent and its tool servers, and
	// removes the tool servers the agent no longer has.
	EnsureAgent(ctx context.Context, a Agent) error
	// RenderedHash returns the hash of the rendering the engine holds, empty
	// when there is none: a different hash means EnsureAgent would restart
	// the agent.
	RenderedHash(ctx context.Context, namespace, name string) (string, error)
	// Hash returns the hash EnsureAgent records for a rendering.
	Hash(a Agent) string
	// AgentReadiness reports whether the engine runs the agent.
	AgentReadiness(ctx context.Context, namespace, name string) (Readiness, error)
	// DeleteAgent removes every object of the agent; it is idempotent.
	DeleteAgent(ctx context.Context, namespace, name string) error
	// Endpoint locates the agent's A2A server for the gateway's route.
	Endpoint(namespace, name string) Endpoint
	// AgentTypes lists the kinds EnsureAgent writes, for the controller to
	// watch.
	AgentTypes() []schema.GroupVersionKind
}

// Engine is everything the controllers need from the engine.
type Engine interface {
	Models
	Agents
	Runs
}

// Run is one run of an agent: a rendered message, never what triggered it
// (spec 06 §2).
type Run struct {
	// Namespace and Agent name the engine's agent (Agent.Namespace, Name).
	Namespace string
	Agent     string
	// GatewayURL is the gateway's base URL: the run goes through the role's
	// A2A route (ADR 0009), built from Endpoint, never read from a card.
	GatewayURL string
	// Message is what the agent is asked.
	Message string
	// ConversationID ties the run's spans and the gateway's together
	// (ADR 0006): the record's name.
	ConversationID string
}

// Outcome is how a run ended, as the engine reports it.
type Outcome string

// Outcomes of a run.
const (
	// OutcomeCompleted: the agent answered; Result.Text is the answer.
	OutcomeCompleted Outcome = "Completed"
	// OutcomeFailed: the run failed; Result.Reason says why.
	OutcomeFailed Outcome = "Failed"
	// OutcomeFailedAfterAnswer: the engine reported a failure although the
	// agent had produced an answer (seen with kagent in spike 001). The text
	// is kept for the record and never published; counted on its own.
	OutcomeFailedAfterAnswer Outcome = "FailedAfterAnswer"
	// OutcomeLost: the engine no longer knows the task (a restart lost it).
	OutcomeLost Outcome = "Lost"
)

// Result is the end of a run.
type Result struct {
	Outcome Outcome
	// Text is the agent's answer, or what it produced before a failure.
	Text string
	// Reason explains a failure, in the engine's words.
	Reason string
}

// CancelResult is what a cancellation achieved.
type CancelResult string

// Cancellation results.
const (
	// CancelDone: the engine stopped the run.
	CancelDone CancelResult = "canceled"
	// CancelUnsupported: the engine cannot stop a run; it goes on to its
	// end and its result is discarded (kagent 0.10.3's python runtime).
	CancelUnsupported CancelResult = "unsupported"
	// CancelFinished: the run had already ended.
	CancelFinished CancelResult = "finished"
)

// ErrTaskUnknown is returned by Poll when the engine does not know the task.
var ErrTaskUnknown = errors.New("the engine does not know the task")

// Runs starts, follows and cancels runs. A run is known by the task ID
// Start returns, which the caller records before following it, so that a
// restart can find the run again.
type Runs interface {
	// Start sends the run and returns as soon as the engine accepted it.
	Start(ctx context.Context, r Run) (taskID string, err error)
	// Poll reads the task once: done is false while it runs. A task the
	// engine does not know returns ErrTaskUnknown; any other error is
	// transient (a cut connection is not a lost task).
	Poll(ctx context.Context, r Run, taskID string) (res Result, done bool, err error)
	// Cancel asks the engine to stop the run.
	Cancel(ctx context.Context, r Run, taskID string) (CancelResult, error)
}
