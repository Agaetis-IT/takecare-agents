// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package investigation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/controller/connector"
	"github.com/agaetis-it/takecare-agents/internal/doc"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/reportslack"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
)

// Publication's retries (spec 02 §4): three after the first attempt.
const (
	publishRetries = 3
	publishBackoff = 2 * time.Second
)

// Poster calls the report connector's tool: internal/mcp in production.
type Poster interface {
	Call(ctx context.Context, ep mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error)
}

// Accounting is the part of the worker that publishes.
type Accounting struct {
	Poster Poster
	// Backoff is the first wait between two publication attempts; it
	// doubles. publishBackoff when zero.
	Backoff time.Duration
	// Duration observes alert accepted to diagnosis published
	// (takecare_investigation_duration_seconds).
	Duration *prometheus.HistogramVec
}

// NewDurationMetric creates and registers takecare_investigation_duration_seconds.
func NewDurationMetric(reg prometheus.Registerer) (*prometheus.HistogramVec, error) {
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "takecare_investigation_duration_seconds",
		Help:    "Time from the alert's acceptance to the diagnosis' publication.",
		Buckets: []float64{15, 30, 60, 90, 120, 180, 300, 600},
	}, []string{"role"})
	if err := reg.Register(h); err != nil {
		if already, ok := errors.AsType[prometheus.AlreadyRegisteredError](err); ok {
			if existing, ok := already.ExistingCollector.(*prometheus.HistogramVec); ok {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("register the duration metric: %w", err)
	}
	return h, nil
}

// publish posts a completed investigation's diagnosis through the Doc's
// report connector, to the destination its level gives (spec 02 §4); the
// model never decides where or whether. Three retries with backoff; the
// outcome goes to status.published, a final failure to a warning event. A
// record kept in status only says so in status.published too, so that a
// record without it is one the operator still has to publish (resume).
func (w *Worker) publish(ctx context.Context, inv *v1alpha1.TakeCareInvestigation, d *v1alpha1.TakeCareDoc, span trace.Span, sent map[string]bool) {
	destination, ok := doc.Destination(d, inv)
	if !ok {
		reason := "status only: the trial level has no destination"
		if inv.Spec.Silent {
			reason = "status only: a silent investigation is never published"
		}
		if _, err := w.patch(ctx, client.ObjectKeyFromObject(inv), func(i *v1alpha1.TakeCareInvestigation) {
			i.Status.Published = &v1alpha1.Publication{OK: true, Reason: reason}
		}); err != nil {
			log.FromContext(ctx).Error(err, "record the status-only publication", "investigation", inv.Name)
		}
		return
	}
	// Usage is measured from PR 4c-2b (spec 02 §5).
	pub := doc.NewPublication(inv, w.clock().Now(), "not measured yet", sent)
	result := v1alpha1.Publication{Connector: d.Spec.Connectors.Report, Destination: destination}
	err := w.post(ctx, d, destination, pub)
	if err != nil && ctx.Err() != nil {
		return // the operator stops: the next leader publishes (resume)
	}
	if err == nil {
		result.OK = true
	} else {
		result.Reason = err.Error()
	}
	updated, perr := w.patch(ctx, client.ObjectKeyFromObject(inv), func(i *v1alpha1.TakeCareInvestigation) { i.Status.Published = &result })
	if perr != nil {
		log.FromContext(ctx).Error(perr, "record the publication", "investigation", inv.Name)
		updated = *inv
	}
	if err != nil {
		w.Recorder.Eventf(&updated, nil, corev1.EventTypeWarning, "PublicationFailed", "Publish", "%s", result.Reason)
		return
	}
	span.AddEvent(semconv.EventDiagnosisPublished, trace.WithAttributes(attribute.String("destination", destination)))
	if w.Accounting.Duration != nil {
		w.Accounting.Duration.WithLabelValues(doc.Kind).Observe(w.clock().Now().Sub(inv.CreationTimestamp.Time).Seconds())
	}
}

// post calls post_message, retrying a failed attempt.
func (w *Worker) post(ctx context.Context, d *v1alpha1.TakeCareDoc, destination string, pub doc.Publication) error {
	var c v1alpha1.TakeCareConnector
	if err := w.Client.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: d.Spec.Connectors.Report}, &c); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("report connector %s does not exist", d.Spec.Connectors.Report)
		}
		return fmt.Errorf("read report connector %s: %w", d.Spec.Connectors.Report, err)
	}
	ep, err := connector.ReportEndpoint(ctx, w.Client, &c)
	if err != nil {
		return err
	}
	blocks, err := json.Marshal(pub.Blocks())
	if err != nil {
		return fmt.Errorf("render the message: %w", err)
	}
	args := map[string]any{"destination": destination, "blocks": json.RawMessage(blocks), "text": pub.Text()}
	backoff := w.Accounting.Backoff
	if backoff <= 0 {
		backoff = publishBackoff
	}
	var last error
	for attempt := 0; attempt <= publishRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("publish: %w", ctx.Err())
			case <-w.clock().After(backoff):
			}
			backoff *= 2
		}
		res, err := w.Accounting.Poster.Call(ctx, ep, reportslack.ToolName, args)
		switch {
		case err != nil:
			last = err
		case res.IsError:
			last = fmt.Errorf("the report server refused the message: %s", res.Text)
		default:
			return nil
		}
	}
	return fmt.Errorf("publish to %s after %d attempts: %w", destination, publishRetries+1, last)
}
