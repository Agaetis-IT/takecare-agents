// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package alerts

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// Defaults and names the chart and the intake agree on (spec 02 §1).
const (
	MaxBody         = 1 << 20
	TokenSecretName = "takecare-alerting"
	TokenSecretKey  = "token"
	roleDoc         = "doc"
	defaultCooldown = 15 * time.Minute
)

// Reasons an alert is not turned into a new investigation.
const (
	ReasonOutOfScope  = "out_of_scope"
	ReasonNotMatched  = "not_matched"
	ReasonNoRole      = "no_role"
	ReasonSuspended   = "suspended"
	ReasonCooldown    = "cooldown"
	ReasonGrouped     = "grouped"
	ReasonDuplicate   = "duplicate"
	ReasonNotFiring   = "not_firing"
	ReasonNoNamespace = "no_namespace"
)

// Metrics of the intake (spec 02, "Metrics").
type Metrics struct {
	Received prometheus.Counter
	Ignored  *prometheus.CounterVec
	Created  prometheus.Counter
}

// NewMetrics creates and registers the intake's metrics.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	received, err := register(reg, prometheus.NewCounter(prometheus.CounterOpts{Name: "takecare_alerts_received_total", Help: "Alerts received on the webhook."}))
	if err != nil {
		return nil, err
	}
	ignored, err := register(reg, prometheus.NewCounterVec(prometheus.CounterOpts{Name: "takecare_alerts_ignored_total", Help: "Alerts that created no investigation, by reason."}, []string{"reason"}))
	if err != nil {
		return nil, err
	}
	created, err := register(reg, prometheus.NewCounter(prometheus.CounterOpts{Name: "takecare_investigations_created_total", Help: "Investigations created from alerts."}))
	if err != nil {
		return nil, err
	}
	return &Metrics{Received: received, Ignored: ignored, Created: created}, nil
}

// register registers a collector, or returns the one already registered
// under the same name (the manager is built once per process, tests build
// it more often).
func register[C prometheus.Collector](reg prometheus.Registerer, c C) (C, error) {
	if err := reg.Register(c); err != nil {
		if already, ok := errors.AsType[prometheus.AlreadyRegisteredError](err); ok {
			if existing, ok := already.ExistingCollector.(C); ok {
				return existing, nil
			}
		}
		return c, fmt.Errorf("register alert metrics: %w", err)
	}
	return c, nil
}

// Handler serves POST /v1/alerts. Every replica serves it: creating the
// record is idempotent, the run happens on the leader.
type Handler struct {
	// Client is the manager's client: it reads the token Secret and the
	// Docs from the cache (the operator namespace is cached, so a webhook
	// costs no call to the API server and a rotated Secret is seen without
	// a restart), and writes investigations.
	Client client.Client
	// TokenSecret names the Secret holding the bearer token in the
	// operator namespace (key TokenSecretKey); empty means TokenSecretName.
	// The chart sets it from alerting.existingSecret.
	TokenSecret string
	// Reader reads investigations for deduplication and grouping straight
	// from the API server: the cache may not hold yet the record created a
	// moment ago, and two alerts of one incident often arrive in the same
	// webhook (the smoke run showed it).
	Reader    client.Reader
	Namespace string
	Metrics   *Metrics
	// Now is replaceable in tests.
	Now func() time.Time
}

// ServeHTTP authenticates, parses and records; it answers 202 once the
// records exist, the work being asynchronous.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := log.FromContext(ctx).WithName("alerts")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := h.authenticate(ctx, r); err != nil {
		logger.V(1).Info("rejected", "reason", err.Error())
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil || len(body) > MaxBody {
		http.Error(w, "payload too large or unreadable", http.StatusBadRequest)
		return
	}
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "malformed Alertmanager payload", http.StatusBadRequest)
		return
	}
	// An error on one alert answers 503 although the alerts before it are
	// recorded: Alertmanager then resends the whole group, which is
	// idempotent by construction (the records' names are deterministic, a
	// resent alert is a duplicate). The metrics therefore count deliveries,
	// not distinct alerts (spec 02 §1).
	seen := map[string]*v1alpha1.TakeCareInvestigation{}
	for _, a := range p.Alerts {
		h.Metrics.Received.Inc()
		if err := h.handle(ctx, a, seen); err != nil {
			logger.Error(err, "alert not recorded", "alertname", a.Labels[labelAlertName], "fingerprint", a.Fingerprint)
			http.Error(w, "alert not recorded, retry", http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

// authenticate checks the bearer token against the Secret the chart
// generates, in constant time.
func (h *Handler) authenticate(ctx context.Context, r *http.Request) error {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || got == "" {
		return errors.New("no bearer token")
	}
	name := h.TokenSecret
	if name == "" {
		name = TokenSecretName
	}
	var s corev1.Secret
	// From the cache, never the API reader: see Client.
	if err := h.Client.Get(ctx, client.ObjectKey{Namespace: h.Namespace, Name: name}, &s); err != nil {
		return fmt.Errorf("token Secret: %w", err)
	}
	want := s.Data[TokenSecretKey]
	if len(want) == 0 || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
		return errors.New("wrong token")
	}
	return nil
}

func (h *Handler) ignore(reason string) { h.Metrics.Ignored.WithLabelValues(reason).Inc() }

// handle turns one alert into a record, or not, and says why not. seen
// holds the investigations this webhook created, by incident key.
func (h *Handler) handle(ctx context.Context, a Alert, seen map[string]*v1alpha1.TakeCareInvestigation) error {
	ns := a.Labels[labelNamespace]
	if a.Status == "resolved" {
		h.ignore(ReasonNotFiring)
		return h.resolve(ctx, a)
	}
	if a.Status != "firing" {
		h.ignore(ReasonNotFiring)
		return nil
	}
	if ns == "" {
		h.ignore(ReasonNoNamespace)
		return nil
	}
	doc, reason, err := h.docFor(ctx, a)
	if err != nil {
		return err
	}
	if doc == nil {
		h.ignore(reason)
		return nil
	}
	name := InvestigationName(roleDoc, a.Fingerprint, a.StartsAt)
	var existing v1alpha1.TakeCareInvestigation
	switch err := h.reader().Get(ctx, client.ObjectKey{Namespace: h.Namespace, Name: name}, &existing); {
	case err == nil:
		h.ignore(ReasonDuplicate)
		return nil
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("get investigation %s: %w", name, err)
	}
	cooldown := defaultCooldown
	if c := doc.Spec.Investigation.Cooldown; c != nil {
		cooldown = c.Duration
	}
	since := h.now().Add(-cooldown)
	if recent, err := h.recent(ctx, v1alpha1.LabelFingerprint, labelSafe(a.Fingerprint), since); err != nil {
		return err
	} else if recent != nil {
		h.ignore(ReasonCooldown)
		return nil
	}
	workload := Workload(a.Labels)
	key := IncidentKey(ns, workload)
	if inv, ok := seen[key]; ok {
		h.ignore(ReasonGrouped)
		return h.fold(ctx, inv, a)
	}
	if recent, err := h.recent(ctx, LabelIncident, key, since); err != nil {
		return err
	} else if recent != nil {
		h.ignore(ReasonGrouped)
		return h.fold(ctx, recent, a)
	}
	inv := &v1alpha1.TakeCareInvestigation{
		Name: name, Namespace: h.Namespace,
		Labels: map[string]string{
			v1alpha1.LabelRole:           roleDoc,
			v1alpha1.LabelFingerprint:    labelSafe(a.Fingerprint),
			v1alpha1.LabelAlertNamespace: labelSafe(ns),
			v1alpha1.LabelAlertName:      labelSafe(a.Labels[labelAlertName]),
			LabelIncident:                key,
		},
		Spec: v1alpha1.TakeCareInvestigationSpec{
			RoleRef: v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareDoc, Name: doc.Name},
			Alert: v1alpha1.AlertRecord{
				Name: a.Labels[labelAlertName], Fingerprint: a.Fingerprint,
				Service: a.Labels[labelService], Namespace: ns, Workload: workload,
				FiredAt: metav1.NewTime(a.StartsAt), Labels: a.Labels, Annotations: a.Annotations,
			},
			Level:  effectiveLevel(doc.Spec.Level),
			Silent: a.Labels[labelSilent] == "true",
		},
	}
	if err := h.Client.Create(ctx, inv); err != nil {
		if apierrors.IsAlreadyExists(err) {
			h.ignore(ReasonDuplicate)
			return nil
		}
		return fmt.Errorf("create investigation %s: %w", name, err)
	}
	h.Metrics.Created.Inc()
	seen[key] = inv
	return nil
}

func (h *Handler) reader() client.Reader {
	if h.Reader != nil {
		return h.Reader
	}
	return h.Client
}

// LabelIncident carries the incident key on an investigation.
const LabelIncident = "takecare.agaetis.fr/incident"

// docFor finds the Doc whose scope and matchers take the alert. Milestone 1
// runs one Doc per cluster (spec 02); when several Docs' scopes overlap, the
// first by name takes the alert, so that the choice is deterministic.
func (h *Handler) docFor(ctx context.Context, a Alert) (*v1alpha1.TakeCareDoc, string, error) {
	var docs v1alpha1.TakeCareDocList
	if err := h.Client.List(ctx, &docs, client.InNamespace(h.Namespace)); err != nil {
		return nil, "", fmt.Errorf("list TakeCareDocs: %w", err)
	}
	if len(docs.Items) == 0 {
		return nil, ReasonNoRole, nil
	}
	slices.SortFunc(docs.Items, func(x, y v1alpha1.TakeCareDoc) int { return strings.Compare(x.Name, y.Name) })
	reason := ReasonOutOfScope
	for i := range docs.Items {
		d := &docs.Items[i]
		if !slices.Contains(d.Spec.Scope.Namespaces, v1alpha1.Namespace(a.Labels[labelNamespace])) {
			continue
		}
		if !matches(d.Spec.Alerts.Matchers, a.Labels) {
			reason = ReasonNotMatched
			continue
		}
		if d.Spec.Suspend {
			reason = ReasonSuspended
			continue
		}
		return d, "", nil
	}
	return nil, reason, nil
}

func matches(matchers []v1alpha1.AlertMatcher, labels map[string]string) bool {
	for _, m := range matchers {
		if labels[m.Name] != m.Value {
			return false
		}
	}
	return true
}

// recent returns the newest investigation carrying the label value created
// after since, if any. It lists every record of the label value: cheap with
// the 7-day retention of milestone 1; a longer retention (the Kiné's
// history, spec 06 §7) will need a cache index on the label and the
// creation time instead.
func (h *Handler) recent(ctx context.Context, label, value string, since time.Time) (*v1alpha1.TakeCareInvestigation, error) {
	var list v1alpha1.TakeCareInvestigationList
	if err := h.reader().List(ctx, &list, client.InNamespace(h.Namespace), client.MatchingLabels{label: value}); err != nil {
		return nil, fmt.Errorf("list investigations by %s: %w", label, err)
	}
	var newest *v1alpha1.TakeCareInvestigation
	for i := range list.Items {
		inv := &list.Items[i]
		if inv.CreationTimestamp.Time.Before(since) {
			continue
		}
		if newest == nil || inv.CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = inv
		}
	}
	return newest, nil
}

// fold records an alert of the same incident on the existing investigation.
func (h *Handler) fold(ctx context.Context, inv *v1alpha1.TakeCareInvestigation, a Alert) error {
	if inv.Spec.Alert.Fingerprint == a.Fingerprint || slices.ContainsFunc(inv.Status.Alerts, func(g v1alpha1.GroupedAlert) bool { return g.Fingerprint == a.Fingerprint }) {
		return nil
	}
	if len(inv.Status.Alerts) >= 50 {
		return nil
	}
	patch := client.MergeFrom(inv.DeepCopy())
	inv.Status.Alerts = append(inv.Status.Alerts, v1alpha1.GroupedAlert{Name: a.Labels[labelAlertName], Fingerprint: a.Fingerprint, FiredAt: metav1.NewTime(a.StartsAt)})
	if err := h.Client.Status().Patch(ctx, inv, patch); err != nil {
		return fmt.Errorf("fold alert into %s: %w", inv.Name, err)
	}
	return nil
}

// resolve stamps resolvedAt on the investigation of a resolved alert.
func (h *Handler) resolve(ctx context.Context, a Alert) error {
	var inv v1alpha1.TakeCareInvestigation
	name := InvestigationName(roleDoc, a.Fingerprint, a.StartsAt)
	if err := h.Client.Get(ctx, client.ObjectKey{Namespace: h.Namespace, Name: name}, &inv); err != nil {
		return client.IgnoreNotFound(err)
	}
	if inv.Status.ResolvedAt != nil {
		return nil
	}
	patch := client.MergeFrom(inv.DeepCopy())
	now := metav1.NewTime(h.now())
	inv.Status.ResolvedAt = &now
	if err := h.Client.Status().Patch(ctx, &inv, patch); err != nil {
		return fmt.Errorf("resolve %s: %w", name, err)
	}
	return nil
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// effectiveLevel is the level recorded at creation; Expert behaves as
// Practitioner in milestone 1 (spec 02 §7).
func effectiveLevel(l v1alpha1.Level) v1alpha1.Level {
	if l == "" {
		return v1alpha1.LevelApprentice
	}
	return l
}

// labelSafe trims a value to what a label accepts.
var labelUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func labelSafe(v string) string {
	v = labelUnsafe.ReplaceAllString(v, "_")
	if len(v) > 63 {
		v = v[:63]
	}
	return strings.Trim(v, "._-")
}
