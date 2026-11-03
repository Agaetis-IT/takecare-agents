// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/apply"
	"github.com/agaetis-it/takecare-agents/internal/demo/corpus"
)

// The corpus ConfigMaps record what they hold, so that a reconcile can
// generate the same corpus again and compare.
const (
	annotationScene = "takecare.agaetis.fr/scenario"
	annotationTime  = "takecare.agaetis.fr/corpus-time"
	corpusKey       = "data.json"
)

// CorpusConfigMap names the ConfigMap a corpus server mounts (the chart's
// demoCorpus add-on).
func CorpusConfigMap(source string) string { return "takecare-demo-corpus-" + source }

// corpusScenario is the corpus of a phase: the incident's, or none.
func corpusScenario(incident bool) string {
	if incident {
		return corpus.ScenarioProxyTimeout
	}
	return corpus.ScenarioNone
}

// scenarioName records a corpus's scenario; "none" for the healthy one.
func scenarioName(scenario string) string {
	if scenario == corpus.ScenarioNone {
		return "none"
	}
	return scenario
}

// corpusConfigMaps is one ConfigMap per source, dated relative to at.
// Generation is deterministic: the same scenario and time give the same
// content, which is how drift is told.
func corpusConfigMaps(d *v1alpha1.TakeCareDemo, scenario string, at time.Time) ([]*corev1.ConfigMap, error) {
	c, err := corpus.Generate(scenario, at)
	if err != nil {
		return nil, fmt.Errorf("generate the corpus: %w", err)
	}
	out := make([]*corev1.ConfigMap, 0, len(corpus.Sources))
	for _, source := range corpus.Sources {
		raw, err := json.MarshalIndent(c[source], "", "  ")
		if err != nil {
			return nil, fmt.Errorf("encode the %s corpus: %w", source, err)
		}
		out = append(out, &corev1.ConfigMap{
			APIVersion: "v1", Kind: "ConfigMap",
			Name: CorpusConfigMap(source), Namespace: d.Namespace, Labels: ownerLabels(d),
			Annotations: map[string]string{annotationScene: scenarioName(scenario), annotationTime: at.UTC().Format(time.RFC3339)},
			Data:        map[string]string{corpusKey: string(raw)},
		})
	}
	return out, nil
}

// syncCorpus writes the corpus of the phase, dated now, when an action asks
// for it, when a ConfigMap is missing or holds another phase's corpus. A
// ConfigMap whose content differs from what its own scenario and time
// generate was edited: that is drift, reported, and the corpus is written
// again with the same time, so that its dates do not move.
func (r *Reconciler) syncCorpus(ctx context.Context, d *v1alpha1.TakeCareDemo, incident, action bool, now time.Time) ([]string, error) {
	scenario := corpusScenario(incident)
	at, rewrite := now, action
	var drift []string
	if !action {
		var current []*corev1.ConfigMap
		for _, source := range corpus.Sources {
			var cm corev1.ConfigMap
			err := r.Reader.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: CorpusConfigMap(source)}, &cm)
			switch {
			case apierrors.IsNotFound(err):
				rewrite = true
			case err != nil:
				return nil, fmt.Errorf("read the %s corpus: %w", source, err)
			case cm.Annotations[annotationScene] != scenarioName(scenario):
				rewrite = true
			default:
				current = append(current, &cm)
			}
		}
		if !rewrite && len(current) > 0 {
			recorded, err := time.Parse(time.RFC3339, current[0].Annotations[annotationTime])
			if err != nil {
				recorded, rewrite = now, true
			}
			want, err := corpusConfigMaps(d, scenario, recorded)
			if err != nil {
				return nil, err
			}
			for i, cm := range current {
				if cm.Annotations[annotationTime] != want[i].Annotations[annotationTime] || cm.Data[corpusKey] != want[i].Data[corpusKey] {
					drift = append(drift, fmt.Sprintf("corpus %s differs from the %s corpus of %s", cm.Name, scenarioName(scenario), recorded.UTC().Format(time.RFC3339)))
				}
			}
			at, rewrite = recorded, rewrite || len(drift) > 0
		}
	}
	if !rewrite {
		return nil, nil
	}
	cms, err := corpusConfigMaps(d, scenario, at)
	if err != nil {
		return nil, err
	}
	for _, cm := range cms {
		if err := controllerutil.SetOwnerReference(d, cm, r.Client.Scheme()); err != nil {
			return nil, fmt.Errorf("own the %s corpus: %w", cm.Name, err)
		}
		if err := apply.Strict(ctx, r.Client, cm); err != nil {
			return nil, fmt.Errorf("apply the corpus %s: %w", cm.Name, err)
		}
	}
	return drift, nil
}
