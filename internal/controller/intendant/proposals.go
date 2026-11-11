// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/discovery"
)

// The operator writes a survey's proposals ConfigMap in its own namespace,
// and reads it there through the API reader: ConfigMaps are not cached in
// the operator's namespace, so no list or watch is needed.
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;create;delete,namespace=takecare-system

const surveyKind = "TakeCareSurvey"

// maxProposalsData bounds a proposals ConfigMap's data, under the 1 MiB
// of an object with room for its metadata.
const maxProposalsData = 900 << 10

// errForeign is a ConfigMap of the proposals' name that no survey of that
// name owns: the operator never touches it.
var errForeign = errors.New("a ConfigMap of that name is not the survey's")

// proposalsName is the name of a survey's proposals ConfigMap; a survey
// name too long for it is shortened with a hash, so that it stays unique.
func proposalsName(survey string) string {
	const prefix, suffix = "survey-", "-proposals"
	if len(prefix)+len(survey)+len(suffix) <= 253 {
		return prefix + survey + suffix
	}
	sum := sha256.Sum256([]byte(survey))
	return prefix + survey[:253-len(prefix)-len(suffix)-17] + "-" + hex.EncodeToString(sum[:])[:16] + suffix
}

// proposalsData is the full set of proposals as the ConfigMap holds it,
// each by its id, as the status writes it, in the import's order, until
// the data reaches maxProposalsData. When some are left out, the layer of
// the first one left out says so in its not-seen lines, first, so that
// the status's bound never drops it.
func proposalsData(res *discovery.Result, name string) (map[string]string, error) {
	data := make(map[string]string, len(res.Proposals))
	size := 0
	for i, p := range res.Proposals {
		b, err := yaml.Marshal(v1alpha1.Proposal{
			ID: p.ID, Action: p.Action, Key: v1alpha1.EntityKey(p.Key), Entry: p.Entry, Evidence: p.Evidence, Source: p.Source,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal proposal %s: %w", p.ID, err)
		}
		if size+len(p.ID)+len(b) > maxProposalsData {
			left := discovery.NotSeen{Ref: name, Reason: fmt.Sprintf("the full set exceeds the ConfigMap's size: %d proposals left out", len(res.Proposals)-i)}
			for j := range res.Layers {
				if res.Layers[j].Name == p.Layer {
					res.Layers[j].NotSeen = append([]discovery.NotSeen{left}, res.Layers[j].NotSeen...)
				}
			}
			break
		}
		data[p.ID], size = string(b), size+len(p.ID)+len(b)
	}
	return data, nil
}

// recordProposals writes a survey's proposals ConfigMap, owned by the
// survey. A run of the survey again replaces the one an earlier run left:
// deleted, then created, the operator having no update on ConfigMaps.
func (w *Runner) recordProposals(ctx context.Context, s *v1alpha1.TakeCareSurvey, name string, data map[string]string) error {
	key := client.ObjectKey{Namespace: s.Namespace, Name: name}
	var cur corev1.ConfigMap
	switch err := w.Reader.Get(ctx, key, &cur); {
	case apierrors.IsNotFound(err):
	case err != nil:
		return fmt.Errorf("read the ConfigMap %s: %w", name, err)
	case !ownedBySurvey(&cur, s.Name):
		return fmt.Errorf("%w: %s", errForeign, name)
	default:
		uid := cur.UID
		if err := w.Client.Delete(ctx, &cur, client.Preconditions{UID: &uid}); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete the ConfigMap %s: %w", name, err)
		}
	}
	cm := &corev1.ConfigMap{
		Name: name, Namespace: s.Namespace,
		Labels:          map[string]string{"app.kubernetes.io/managed-by": "takecare-operator", "takecare.agaetis.fr/survey": labelValue(s.Name)},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind(surveyKind))},
		Data:            data,
	}
	if err := w.Client.Create(ctx, cm); err != nil {
		return fmt.Errorf("create the ConfigMap %s: %w", name, err)
	}
	return nil
}

// ownedBySurvey says whether a ConfigMap is a survey's of that name: this
// one, or an earlier one of the same name not yet collected.
func ownedBySurvey(cm *corev1.ConfigMap, survey string) bool {
	for _, o := range cm.OwnerReferences {
		if o.Kind == surveyKind && o.Name == survey && o.Controller != nil && *o.Controller {
			return true
		}
	}
	return false
}

// labelValue is a survey's name as a label value, at most 63 characters.
func labelValue(name string) string {
	if len(name) <= 63 {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return name[:46] + "-" + hex.EncodeToString(sum[:])[:16]
}
