// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package alerts

import (
	"context"
	"fmt"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

// lagging is the manager's cache a moment behind the API server: it does
// not list the investigations created through it yet.
type lagging struct {
	client.Client
	mu     sync.Mutex
	hidden map[string]bool
}

func (l *lagging) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := l.Client.Create(ctx, obj, opts...); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hidden[obj.GetName()] = true
	return nil
}

func (l *lagging) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := l.Client.List(ctx, list, opts...); err != nil {
		return err
	}
	if invs, ok := list.(*v1alpha1.TakeCareInvestigationList); ok {
		l.mu.Lock()
		defer l.mu.Unlock()
		invs.Items = slices.DeleteFunc(invs.Items, func(i v1alpha1.TakeCareInvestigation) bool { return l.hidden[i.Name] })
	}
	return nil
}

// stamping sets the creation time the API server would, which the fake
// client leaves empty.
type stamping struct{ client.Client }

func (s stamping) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if at := obj.GetCreationTimestamp(); at.IsZero() {
		obj.SetCreationTimestamp(metav1.NewTime(time.Now().Truncate(time.Second)))
	}
	return s.Client.Create(ctx, obj, opts...)
}

// spy counts the investigation lists a reader serves, by whether they
// went through IndexRecent.
type spy struct {
	client.Reader
	mu               sync.Mutex
	indexed, scanned int
}

func (s *spy) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*v1alpha1.TakeCareInvestigationList); ok {
		var o client.ListOptions
		o.ApplyOptions(opts)
		s.mu.Lock()
		if o.FieldSelector != nil && slices.ContainsFunc(o.FieldSelector.Requirements(), func(r fields.Requirement) bool { return r.Field == IndexRecent }) {
			s.indexed++
		} else {
			s.scanned++
		}
		s.mu.Unlock()
	}
	return s.Reader.List(ctx, list, opts...)
}

func (s *spy) counts() (indexed, scanned int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.indexed, s.scanned
}

// severityCritical is the severity the intake's Doc matches.
const severityCritical = "critical"

// intakeDoc is the Doc of start(), for the fake client.
func intakeDoc() *v1alpha1.TakeCareDoc {
	return &v1alpha1.TakeCareDoc{
		Name: roleDoc, Namespace: ns,
		Spec: v1alpha1.TakeCareDocSpec{
			RoleCommonSpec: v1alpha1.RoleCommonSpec{Level: v1alpha1.LevelPractitioner, Scope: v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{appNS}}},
			Alerts:         v1alpha1.AlertsSpec{Matchers: []v1alpha1.AlertMatcher{{Name: sevLabel, Value: severityCritical}}},
		},
	}
}

// startIndexed is an indexed intake over a fake API server, behind a
// cache that does not show what the intake created.
func startIndexed(t *testing.T, objs ...client.Object) (*fixture, *spy, client.Client) {
	t.Helper()
	api := stamping{fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(append(objs,
			&corev1.Secret{Name: TokenSecretName, Namespace: ns, Data: map[string][]byte{TokenSecretKey: []byte(token)}},
			intakeDoc())...).
		WithStatusSubresource(&v1alpha1.TakeCareInvestigation{}).
		WithIndex(&v1alpha1.TakeCareInvestigation{}, IndexRecent, recentKeys).
		Build()}
	metrics, err := NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	live := &spy{Reader: api}
	f := &fixture{t: t, c: api, metrics: metrics, now: time.Now()}
	f.serveIndexed(&lagging{Client: api, hidden: map[string]bool{}}, live)
	return f, live, api
}

// serveIndexed points f at a new indexed intake, with an empty memo, over
// cached for its lists and live for its reads by name: another replica,
// or the same one after a restart.
func (f *fixture) serveIndexed(cached client.Client, live client.Reader) {
	f.t.Helper()
	f.srv = httptest.NewServer(&Handler{
		Client: cached, Reader: live, Indexed: true,
		Namespace: ns, Metrics: f.metrics, Resolver: f, Now: func() time.Time { return f.now },
	})
	f.t.Cleanup(f.srv.Close)
}

func TestRecentKeys(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   []string
	}{
		{"both labels", map[string]string{v1alpha1.LabelFingerprint: "fp", LabelIncident: "inc", v1alpha1.LabelRole: roleDoc}, []string{v1alpha1.LabelFingerprint + "=fp", LabelIncident + "=inc"}},
		{"the fingerprint only", map[string]string{v1alpha1.LabelFingerprint: "fp"}, []string{v1alpha1.LabelFingerprint + "=fp"}},
		{"neither", map[string]string{v1alpha1.LabelRole: roleDoc}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := recentKeys(&v1alpha1.TakeCareInvestigation{Labels: tc.labels})
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("recentKeys (-want +got):\n%s", diff)
			}
		})
	}
}

// The cache lags the record the intake created a moment ago: two webhooks
// of one incident in a row still give one investigation, and the first
// alert sent again with a new startsAt is still in its cooldown. Nothing
// is listed from the API server.
func TestIndexedIntakeSeesWhatTheCacheLags(t *testing.T) {
	f, live, _ := startIndexed(t)
	t0 := time.Now().Add(-time.Minute).Truncate(time.Second)
	pod := notifPod
	f.post(token, alert("PodCrashLooping", "0b0b0b0b0b0b0b01", pod, t0, nil))
	f.post(token, alert(alertNotReady, "0b0b0b0b0b0b0b02", pod, t0, nil))
	items := f.investigations()
	if len(items) != 1 {
		t.Fatalf("%d investigations for one incident in two webhooks in a row", len(items))
	}
	if a := items[0].Status.Alerts; len(a) != 1 || a[0].Name != alertNotReady || f.ignored(ReasonGrouped) != 1 {
		t.Errorf("status.alerts = %+v, grouped %v", a, f.ignored(ReasonGrouped))
	}
	f.post(token, alert("PodCrashLooping", "0b0b0b0b0b0b0b01", pod, t0.Add(30*time.Second), nil))
	if n := len(f.investigations()); n != 1 || f.ignored(ReasonCooldown) != 1 {
		t.Errorf("re-fired within the cooldown: %d investigations, cooldown %v", n, f.ignored(ReasonCooldown))
	}
	if _, scanned := live.counts(); scanned != 0 {
		t.Errorf("%d investigation lists reached the API server", scanned)
	}
}

// Another replica, or the same one restarted, has an empty memo: once the
// cache shows the first record, the index alone answers the grouping of
// the incident's second alert and the cooldown of the first one fired
// again.
func TestIndexedIntakeWithAnEmptyMemo(t *testing.T) {
	f, live, api := startIndexed(t)
	t0 := time.Now().Add(-time.Minute).Truncate(time.Second)
	pod := notifPod
	f.post(token, alert("PodCrashLooping", "0f0f0f0f0f0f0f01", pod, t0, nil))
	if n := len(f.investigations()); n != 1 {
		t.Fatalf("%d investigations after the first alert", n)
	}
	f.serveIndexed(api, live)
	f.post(token, alert(alertNotReady, "0f0f0f0f0f0f0f02", pod, t0, nil))
	f.post(token, alert("PodCrashLooping", "0f0f0f0f0f0f0f01", pod, t0.Add(30*time.Second), nil))
	items := f.investigations()
	if len(items) != 1 {
		t.Fatalf("%d investigations for one incident across two intakes", len(items))
	}
	if a := items[0].Status.Alerts; len(a) != 1 || a[0].Name != alertNotReady {
		t.Errorf("status.alerts = %+v", a)
	}
	for reason, want := range map[string]float64{ReasonGrouped: 1, ReasonCooldown: 1} {
		if got := f.ignored(reason); got != want {
			t.Errorf("ignored %s = %v, want %v", reason, got, want)
		}
	}
	if _, scanned := live.counts(); scanned != 0 {
		t.Errorf("%d investigation lists reached the API server", scanned)
	}
}

// The memo only covers the cache's lag: a record remembered longer ago
// than cacheLag is the cache's to show.
func TestCreatedMemoMatchesWithinTheLag(t *testing.T) {
	at := time.Now()
	var m createdMemo
	m.add(investigationAt(1, at), at)
	label, value := LabelIncident, "other-1"
	if n := len(m.match(label, value, at.Add(cacheLag))); n != 1 {
		t.Errorf("%d matches at the end of the lag, want 1", n)
	}
	if n := len(m.match(label, value, at.Add(cacheLag+time.Second))); n != 0 {
		t.Errorf("%d matches past the lag, want 0", n)
	}
	if n := len(m.match(label, "other-2", at)); n != 0 {
		t.Errorf("%d matches of another incident, want 0", n)
	}
}

// A record the intake remembers but that was deleted since is not
// believed: deleting investigations clears their cooldown (spec 02 §1).
func TestIndexedIntakeForgetsADeletedRecord(t *testing.T) {
	f, _, api := startIndexed(t)
	t0 := time.Now().Add(-time.Minute).Truncate(time.Second)
	f.post(token, alert("PodCrashLooping", "0c0c0c0c0c0c0c01", "worker-1", t0, nil))
	items := f.investigations()
	if len(items) != 1 {
		t.Fatalf("%d investigations", len(items))
	}
	if err := api.Delete(context.Background(), &items[0]); err != nil {
		t.Fatal(err)
	}
	f.post(token, alert("PodCrashLooping", "0c0c0c0c0c0c0c01", "worker-1", t0.Add(30*time.Second), nil))
	if n := len(f.investigations()); n != 1 || f.ignored(ReasonCooldown) != 0 {
		t.Errorf("after the deletion: %d investigations, cooldown %v", n, f.ignored(ReasonCooldown))
	}
}

// investigationAt is a stored investigation of another incident. Its name
// carries the index: InvestigationName would give these fingerprints, alike
// in their first ten characters, only a 6-hex digest each, and 2,000 of them
// collide in about one fixture in nine.
func investigationAt(i int, at time.Time) *v1alpha1.TakeCareInvestigation {
	fp := fmt.Sprintf("%016x", 0x1000+i)
	return &v1alpha1.TakeCareInvestigation{
		Name: fmt.Sprintf("%s-other-%05d", roleDoc, i), Namespace: ns, CreationTimestamp: metav1.NewTime(at),
		Labels: map[string]string{v1alpha1.LabelRole: roleDoc, v1alpha1.LabelFingerprint: fp, LabelIncident: fmt.Sprintf("other-%d", i)},
		Spec: v1alpha1.TakeCareInvestigationSpec{
			RoleRef: v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareDoc, Name: roleDoc},
			Alert:   v1alpha1.AlertRecord{Name: "Other", Fingerprint: fp, FiredAt: metav1.NewTime(at)},
			Level:   v1alpha1.LevelPractitioner,
		},
	}
}

// keptInvestigations is K1b's default retention count.
const keptInvestigations = 2000

// The scale fixtures name each record uniquely, at an instant where the
// names built from the fingerprint and the firing time collided.
func TestScaleFixtureNamesAreUnique(t *testing.T) {
	at := time.Date(2026, 11, 25, 10, 0, 3, 703701000, time.UTC)
	seen := make(map[string]int, keptInvestigations)
	for i := range keptInvestigations {
		name := investigationAt(i, at).Name
		if j, dup := seen[name]; dup {
			t.Fatalf("records %d and %d are both named %s", j, i, name)
		}
		seen[name] = i
	}
}

// At the retention count, a firing alert reads one fingerprint's and one
// incident's records through the index, never the namespace's.
func TestIndexedIntakeAtRetentionCount(t *testing.T) {
	at := time.Now().Add(-5 * time.Minute)
	objs := make([]client.Object, 0, keptInvestigations)
	for i := range keptInvestigations {
		objs = append(objs, investigationAt(i, at))
	}
	f, live, _ := startIndexed(t, objs...)
	f.post(token, alert("PodCrashLooping", "0d0d0d0d0d0d0d01", "worker-1", time.Now().Truncate(time.Second), nil))
	if n := len(f.investigations()); n != keptInvestigations+1 {
		t.Errorf("%d investigations, want %d", n, keptInvestigations+1)
	}
	if _, scanned := live.counts(); scanned != 0 {
		t.Errorf("%d investigation lists reached the API server", scanned)
	}
}

// envtest: the manager's real cache with the index, 2,000 other
// investigations stored. Deduplication, cooldown and grouping hold, and
// every investigation list is an indexed read of the cache: none reaches
// the API server.
func TestIndexedIntakeOnTheCache(t *testing.T) {
	f := start(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	at := time.Now().Add(-5 * time.Minute)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(16)
	for i := range keptInvestigations {
		g.Go(func() error { return f.c.Create(gctx, investigationAt(i, at)) })
	}
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
	cachedClient, cached := indexedCache(ctx, t, f)
	live := &spy{Reader: f.c}
	f.serveIndexed(cachedClient, live)

	t0 := time.Now().Add(-time.Minute).Truncate(time.Second)
	pod := notifPod
	crash := alert("PodCrashLooping", "0e0e0e0e0e0e0e01", pod, t0, nil)
	f.post(token, crash)
	f.post(token, crash)
	f.post(token, alert(alertNotReady, "0e0e0e0e0e0e0e02", pod, t0, nil))
	f.post(token, alert("PodCrashLooping", "0e0e0e0e0e0e0e01", pod, t0.Add(30*time.Second), nil))
	var mine v1alpha1.TakeCareInvestigationList
	if err := f.c.List(ctx, &mine, client.InNamespace(ns), client.MatchingLabels{LabelIncident: IncidentKey(fromAlertmanager(Payload{Alerts: []Alert{crash}})[0].record)}); err != nil {
		t.Fatal(err)
	}
	if len(mine.Items) != 1 {
		t.Fatalf("%d investigations for the incident", len(mine.Items))
	}
	if a := mine.Items[0].Status.Alerts; len(a) != 1 || a[0].Name != alertNotReady {
		t.Errorf("status.alerts = %+v", a)
	}
	for reason, want := range map[string]float64{ReasonDuplicate: 1, ReasonGrouped: 1, ReasonCooldown: 1} {
		if got := f.ignored(reason); got != want {
			t.Errorf("ignored %s = %v, want %v", reason, got, want)
		}
	}
	if n := len(f.investigations()); n != keptInvestigations+1 {
		t.Errorf("%d investigations, want %d", n, keptInvestigations+1)
	}
	indexed, scanned := cached.counts()
	if indexed == 0 || scanned != 0 {
		t.Errorf("cache: %d indexed lists, %d scans; want indexed lists only", indexed, scanned)
	}
	if indexed, scanned := live.counts(); indexed+scanned != 0 {
		t.Errorf("%d investigation lists reached the API server", indexed+scanned)
	}
}

// indexedCache is a client reading through a started cache of the
// operator namespace holding IndexRecent, as the manager's, and the spy on
// that cache.
func indexedCache(ctx context.Context, t *testing.T, f *fixture) (client.Client, *spy) {
	t.Helper()
	scheme := testScheme(t)
	c, err := cache.New(f.cfg, cache.Options{Scheme: scheme, DefaultNamespaces: map[string]cache.Config{ns: {}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := IndexInvestigations(ctx, c); err != nil {
		t.Fatal(err)
	}
	go func() { _ = c.Start(ctx) }()
	if !c.WaitForCacheSync(ctx) {
		t.Fatal("the cache did not sync")
	}
	cached := &spy{Reader: c}
	cachedClient, err := client.New(f.cfg, client.Options{Scheme: scheme, Cache: &client.CacheOptions{Reader: cached}})
	if err != nil {
		t.Fatal(err)
	}
	return cachedClient, cached
}

// envtest: another replica, or the same one restarted, has an empty memo.
// Once the cache shows the first record, the cache index alone answers
// the grouping of the incident's second alert and the cooldown of the
// first one fired again.
func TestIndexedIntakeOnTheCacheWithAnEmptyMemo(t *testing.T) {
	f := start(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cachedClient, cached := indexedCache(ctx, t, f)
	live := &spy{Reader: f.c}
	f.serveIndexed(cachedClient, live)

	t0 := time.Now().Add(-time.Minute).Truncate(time.Second)
	pod := notifPod
	crash := alert("PodCrashLooping", "0a0a0a0a0a0a0a01", pod, t0, nil)
	f.post(token, crash)
	incident := IncidentKey(fromAlertmanager(Payload{Alerts: []Alert{crash}})[0].record)
	err := wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		var shown v1alpha1.TakeCareInvestigationList
		if err := cachedClient.List(ctx, &shown, client.InNamespace(ns), client.MatchingFields{IndexRecent: LabelIncident + "=" + incident}); err != nil {
			return false, err
		}
		return len(shown.Items) == 1, nil
	})
	if err != nil {
		t.Fatalf("the cache did not show the first record: %v", err)
	}

	f.serveIndexed(cachedClient, live)
	f.post(token, alert(alertNotReady, "0a0a0a0a0a0a0a02", pod, t0, nil))
	f.post(token, alert("PodCrashLooping", "0a0a0a0a0a0a0a01", pod, t0.Add(30*time.Second), nil))
	items := f.investigations()
	if len(items) != 1 {
		t.Fatalf("%d investigations for one incident across two intakes", len(items))
	}
	if a := items[0].Status.Alerts; len(a) != 1 || a[0].Name != alertNotReady {
		t.Errorf("status.alerts = %+v", a)
	}
	for reason, want := range map[string]float64{ReasonGrouped: 1, ReasonCooldown: 1} {
		if got := f.ignored(reason); got != want {
			t.Errorf("ignored %s = %v, want %v", reason, got, want)
		}
	}
	if _, scanned := cached.counts(); scanned != 0 {
		t.Errorf("cache: %d scans; want indexed lists only", scanned)
	}
	if indexed, scanned := live.counts(); indexed+scanned != 0 {
		t.Errorf("%d investigation lists reached the API server", indexed+scanned)
	}
}
