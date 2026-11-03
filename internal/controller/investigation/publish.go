// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/controller/connector"
	"github.com/agaetis-it/takecare-agents/internal/doc"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/reportslack"
	"github.com/agaetis-it/takecare-agents/internal/telemetry/semconv"
	"github.com/agaetis-it/takecare-agents/internal/usage"
)

// ConditionUsageAttributed says whether the record's usage could be
// measured (spec 02 §5): False with the reason when it could not, and the
// usage is then absent, never a wrong number.
const ConditionUsageAttributed = "UsageAttributed"

// Publication's retries (spec 02 §4): three after the first attempt.
const (
	publishRetries = 3
	publishBackoff = 2 * time.Second
)

// Poster calls the report connector's tool: internal/mcp in production.
type Poster interface {
	Call(ctx context.Context, ep mcp.Endpoint, tool string, args map[string]any) (mcp.Result, error)
}

// UsageSource gives the usage source of the platform, nil when it declares
// no Prometheus.
type UsageSource func(p *v1alpha1.TakeCarePlatform) usage.Source

// Accounting is the part of the worker that measures and publishes.
type Accounting struct {
	Usage UsageSource
	// ScrapeWait is how long after a run's end the gateway's last calls are
	// in Prometheus (one scrape interval, spec 02 §5).
	ScrapeWait time.Duration
	Poster     Poster
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

// meter holds a run's start snapshot.
type meter struct {
	source usage.Source
	start  usage.Counters
	// reason is set when the usage cannot be measured.
	reason string
}

// startMeter reads the counters when the run starts. A resumed run lost its
// start snapshot with the previous leader.
func (w *Worker) startMeter(ctx context.Context, resumed bool) *meter {
	m := &meter{}
	p, err := w.platform(ctx)
	switch {
	case resumed:
		m.reason = "the operator restarted during the run: its start was not measured"
		return m
	case w.Accounting.Usage == nil:
		m.reason = "the operator measures no usage"
		return m
	case err != nil || p == nil:
		m.reason = "the platform is not readable"
		return m
	}
	if m.source = w.Accounting.Usage(p); m.source == nil {
		m.reason = "the platform declares no Prometheus (observability.metrics.prometheusURL)"
		return m
	}
	if m.start, err = m.source.Snapshot(ctx); err != nil {
		m.reason = err.Error()
	}
	return m
}

// account measures the run once it ended: one scrape interval later, the
// gateway's counters are read again and the difference priced. When the
// start could not be read, or Prometheus cannot be reached, it does not
// wait: the record says why there is no usage.
func (w *Worker) account(ctx context.Context, inv *v1alpha1.TakeCareInvestigation, d *v1alpha1.TakeCareDoc, m *meter) *v1alpha1.TakeCareInvestigation {
	reason := m.reason
	var rec *v1alpha1.Usage
	if reason == "" {
		select {
		case <-ctx.Done():
			return inv
		case <-w.clock().After(w.Accounting.ScrapeWait):
		}
		end, err := m.source.Snapshot(ctx)
		if err == nil {
			var delta usage.Counters
			if delta, err = usage.Delta(m.start, end); err == nil {
				var u v1alpha1.Usage
				u, err = usage.Record(delta, w.pricing(ctx, d), maxConcurrent(d) > 1)
				rec = &u
			}
		}
		if err != nil {
			reason, rec = err.Error(), nil
		}
	}
	updated, err := w.patch(ctx, client.ObjectKeyFromObject(inv), func(i *v1alpha1.TakeCareInvestigation) {
		i.Status.Usage = rec
		c := metav1.Condition{Type: ConditionUsageAttributed, Status: metav1.ConditionTrue, Reason: "Measured", Message: "usage from the gateway's metrics", ObservedGeneration: i.Generation}
		if rec == nil {
			c.Status, c.Reason, c.Message = metav1.ConditionFalse, "NotMeasured", reason
		} else if rec.Approximate {
			c.Message += "; approximate: the Doc may run investigations at the same time"
		}
		meta.SetStatusCondition(&i.Status.Conditions, c)
	})
	if err != nil {
		log.FromContext(ctx).Error(err, "record the usage", "investigation", inv.Name)
		return inv
	}
	return &updated
}

// pricing is the Doc's model's pricing, nil when it has none.
func (w *Worker) pricing(ctx context.Context, d *v1alpha1.TakeCareDoc) *v1alpha1.Pricing {
	p, err := w.platform(ctx)
	if err != nil || p == nil || len(p.Spec.Models) == 0 {
		return nil
	}
	name := p.Spec.Models[0].Name
	if d.Spec.Model != nil {
		name = d.Spec.Model.Name
	}
	for _, m := range p.Spec.Models {
		if m.Name == name {
			return m.Pricing
		}
	}
	return nil
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
	reason := ""
	switch c := meta.FindStatusCondition(inv.Status.Conditions, ConditionUsageAttributed); {
	case c == nil:
		// A record published by a new leader whose predecessor stopped
		// before the usage was read.
		reason = "the operator restarted before the usage was read"
	case c.Status == metav1.ConditionFalse:
		reason = c.Message
	}
	pub := doc.NewPublication(inv, w.clock().Now(), reason, sent)
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
