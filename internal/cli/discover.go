// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/duration"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/kubeview"
)

// discoverSchemaVersion versions the shape of discover --output json.
const discoverSchemaVersion = 1

// Discover is what discover prints: an Intendant's newest survey, the Map
// revision its map connector serves now, and the command that creates a
// new survey. Survey is nil when the Intendant has none yet.
type Discover struct {
	SchemaVersion  int               `json:"schemaVersion"`
	Namespace      string            `json:"namespace"`
	Intendant      string            `json:"intendant"`
	MapConnector   string            `json:"mapConnector"`
	ServedRevision string            `json:"servedRevision"`
	Survey         *DiscoveredSurvey `json:"survey"`
	Command        string            `json:"command"`
}

// DiscoveredSurvey is a survey as discover shows it. PredatesServedMap is
// set when its Map revision is not the one the map connector serves now.
type DiscoveredSurvey struct {
	RunRecord
	MapRevision       string          `json:"mapRevision"`
	PredatesServedMap bool            `json:"predatesServedMap"`
	Proposals         SurveyProposals `json:"proposals"`
	Layers            []SurveyLayer   `json:"layers"`
	BlindSpots        []BlindSpot     `json:"blindSpots"`
}

// SurveyProposals counts a survey's proposals by action, over the full
// set, and by source, over the Counted proposals its status holds: the
// rest are in the survey's ConfigMap, which discover does not read.
type SurveyProposals struct {
	Add     int32 `json:"add"`
	Update  int32 `json:"update"`
	Remove  int32 `json:"remove"`
	Total   int32 `json:"total"`
	Import  int   `json:"import"`
	Agent   int   `json:"agent"`
	Counted int   `json:"counted"`
}

// SurveyLayer is what a survey found on one layer, and what it could not read.
type SurveyLayer struct {
	Name     string    `json:"name"`
	Seen     int32     `json:"seen"`
	Matched  int32     `json:"matched"`
	Proposed int32     `json:"proposed"`
	NotSeen  []NotSeen `json:"notSeen"`
}

// NotSeen is a part of the ground a survey could not read, and why.
type NotSeen struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

func (a *app) discover(ctx context.Context, args []string) int {
	var (
		c                                      common
		namespace, kubeconfig, kctx, intendant string
	)
	fs := a.newFlags("discover")
	c.register(fs)
	fs.StringVar(&namespace, "namespace", "takecare-system", "the operator's namespace")
	fs.StringVar(&kubeconfig, "kubeconfig", "", "the kubeconfig file (default: KUBECONFIG, then ~/.kube/config)")
	fs.StringVar(&kctx, "context", "", "the kubeconfig context, for this run only")
	fs.StringVar(&intendant, "intendant", "", "the Intendant (default: the only one in the namespace)")
	if code := a.parse(fs, &c, args); code >= 0 {
		return code
	}
	cfg, err := kubeview.Load(kubeconfig, kctx)
	if err != nil {
		return a.fail("discover: %v", err)
	}
	r, err := kubeview.New(cfg)
	if err != nil {
		return a.fail("discover: %v", err)
	}
	d, err := readDiscover(ctx, r, namespace, intendant)
	if err != nil {
		return a.fail("discover: %v", err)
	}
	if c.output == outputJSON {
		err = a.printJSON(d)
	} else {
		err = d.write(a.stdout, a.now())
	}
	if err != nil {
		return a.fail("discover: %v", err)
	}
	return ExitOK
}

// findIntendant reads the Intendant named name, or the only one in ns
// when name is empty; with several, the name is required.
func findIntendant(ctx context.Context, r getLister, ns, name string) (*v1alpha1.TakeCareIntendant, error) {
	if name != "" {
		var in v1alpha1.TakeCareIntendant
		if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &in); err != nil {
			return nil, fmt.Errorf("read the Intendant %s in namespace %s: %w", name, ns, err)
		}
		return &in, nil
	}
	var is v1alpha1.TakeCareIntendantList
	if err := r.List(ctx, &is, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list takecareintendants in namespace %s: %w", ns, err)
	}
	switch len(is.Items) {
	case 0:
		return nil, fmt.Errorf("no TakeCareIntendant in namespace %s", ns)
	case 1:
		return &is.Items[0], nil
	default:
		return nil, fmt.Errorf("%d Intendants in namespace %s: pass --intendant", len(is.Items), ns)
	}
}

// readDiscover reads the Intendant, its newest survey whatever its phase,
// and its map connector. A map connector that does not exist leaves the
// served revision empty; any other read error stops it.
func readDiscover(ctx context.Context, r getLister, ns, name string) (*Discover, error) {
	in, err := findIntendant(ctx, r, ns, name)
	if err != nil {
		return nil, err
	}
	d := &Discover{SchemaVersion: discoverSchemaVersion, Namespace: ns, Intendant: in.Name, Command: surveyCommand(ns, in.Name)}
	if len(in.Spec.Connectors.Map) > 0 {
		d.MapConnector = in.Spec.Connectors.Map[0]
		var k v1alpha1.TakeCareConnector
		err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: d.MapConnector}, &k)
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("read the map connector %s in namespace %s: %w", d.MapConnector, ns, err)
		}
		d.ServedRevision = k.Status.MapRevision
	}
	var ss v1alpha1.TakeCareSurveyList
	if err := r.List(ctx, &ss, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list takecaresurveys in namespace %s: %w", ns, err)
	}
	var s *v1alpha1.TakeCareSurvey
	for i := range ss.Items {
		v := &ss.Items[i]
		if v.Spec.IntendantRef.Name != in.Name {
			continue
		}
		if s == nil || v.CreationTimestamp.After(s.CreationTimestamp.Time) ||
			v.CreationTimestamp.Equal(&s.CreationTimestamp) && v.Name > s.Name {
			s = v
		}
	}
	if s == nil {
		return d, nil
	}
	st := s.Status
	ds := &DiscoveredSurvey{
		RunRecord:   run(s.ObjectMeta, st.RunStatus, string(s.Spec.Trigger)),
		MapRevision: st.MapRevision, PredatesServedMap: st.MapRevision != "" && d.ServedRevision != "" && st.MapRevision != d.ServedRevision,
		Proposals: SurveyProposals{Counted: len(st.Proposals)}, Layers: []SurveyLayer{}, BlindSpots: []BlindSpot{},
	}
	if c := st.ProposalCounts; c != nil {
		ds.Proposals.Add, ds.Proposals.Update, ds.Proposals.Remove, ds.Proposals.Total = c.Add, c.Update, c.Remove, c.Total
	}
	for _, p := range st.Proposals {
		switch p.Source {
		case v1alpha1.ProposalFromImport:
			ds.Proposals.Import++
		case v1alpha1.ProposalFromAgent:
			ds.Proposals.Agent++
		}
	}
	for _, l := range st.Layers {
		sl := SurveyLayer{Name: l.Name, Seen: l.Seen, Matched: l.Matched, Proposed: l.Proposed, NotSeen: []NotSeen{}}
		for _, n := range l.NotSeen {
			sl.NotSeen = append(sl.NotSeen, NotSeen{Ref: n.Ref, Reason: n.Reason})
		}
		ds.Layers = append(ds.Layers, sl)
	}
	for _, b := range st.BlindSpots {
		ds.BlindSpots = append(ds.BlindSpots, BlindSpot{Entity: string(b.Entity), Reason: string(b.Reason), Message: b.Message})
	}
	d.Survey = ds
	return d, nil
}

// surveyCommand is the command that creates an on-demand survey of the
// Intendant name in ns, for the person to run with their own rights.
func surveyCommand(ns, name string) string {
	return fmt.Sprintf(`kubectl -n %[1]s create -f - <<'EOF'
apiVersion: %[3]s
kind: TakeCareSurvey
metadata: { generateName: %[2]s-by-hand-, namespace: %[1]s }
spec: { intendantRef: { kind: %[4]s, name: %[2]s } }
EOF
`, ns, name, v1alpha1.GroupVersion.String(), v1alpha1.KindTakeCareIntendant)
}

// write prints d as text, the age counted from now.
func (d *Discover) write(w io.Writer, now time.Time) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Intendant %s in namespace %s\n", d.Intendant, d.Namespace)
	served := orDash(d.ServedRevision)
	if d.MapConnector != "" {
		served += " (map connector " + d.MapConnector + ")"
	}
	if s := d.Survey; s == nil {
		fmt.Fprintf(&b, "\nNo survey yet. Map revision served now: %s\n", served)
	} else {
		fmt.Fprintf(&b, "\nSURVEY %s\n  phase %s, trigger %s, age %s\n", s.Name, orDash(s.Phase), orDash(s.Trigger),
			duration.HumanDuration(now.Sub(s.Created)))
		if s.Reason != "" {
			fmt.Fprintf(&b, "  reason %s\n", s.Reason)
		}
		fmt.Fprintf(&b, "  Map revision surveyed against: %s; served now: %s\n", orDash(s.MapRevision), served)
		if s.PredatesServedMap {
			b.WriteString("  The survey predates the served Map.\n")
		}
		p := s.Proposals
		fmt.Fprintf(&b, "  proposals %d: add %d, update %d, remove %d\n", p.Total, p.Add, p.Update, p.Remove)
		fmt.Fprintf(&b, "  by source, over the %d the status holds: import %d, agent %d\n", p.Counted, p.Import, p.Agent)
		b.WriteString("\nLAYERS\n")
		if len(s.Layers) == 0 {
			b.WriteString("  none recorded\n")
		}
		for _, l := range s.Layers {
			fmt.Fprintf(&b, "  %s: seen %d, matched %d, proposed %d\n", l.Name, l.Seen, l.Matched, l.Proposed)
			for _, n := range l.NotSeen {
				fmt.Fprintf(&b, "    not seen %s: %s\n", n.Ref, n.Reason)
			}
		}
		b.WriteString("\nBLIND SPOTS\n")
		if len(s.BlindSpots) == 0 {
			b.WriteString("  none\n")
		}
		for _, x := range s.BlindSpots {
			fmt.Fprintf(&b, "  %s: %s%s\n", x.Entity, x.Reason, suffix(x.Message))
		}
	}
	fmt.Fprintf(&b, "\nTo start a new survey, run with your own rights:\n\n%s", d.Command)
	_, err := io.WriteString(w, b.String())
	return err
}
