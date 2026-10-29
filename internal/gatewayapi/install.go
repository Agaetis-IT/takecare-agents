// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

// Package gatewayapi installs the upstream Gateway API CRDs from a copy
// embedded in the operator binary, so that an installation needs no access to
// the Internet (ADR 0002: a Helm hook job applies them server-side).
//
// A cluster often already has these CRDs, installed by another gateway
// implementation, sometimes from the experimental channel or a newer release.
// Overwriting them could downgrade them or remove fields that implementation
// relies on, so the installer compares bundle versions before touching
// anything and refuses to downgrade.
package gatewayapi

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// standardInstall is the release asset standard-install.yaml of the pinned
// Gateway API version (hack/versions.env), unchanged.
//
//go:embed standard-install.yaml
var standardInstall []byte

// Annotations the upstream bundle sets on every CRD.
const (
	AnnotationBundleVersion = "gateway.networking.k8s.io/bundle-version"
	AnnotationChannel       = "gateway.networking.k8s.io/channel"
)

// fieldOwner is the server-side apply field manager of the hook job.
const fieldOwner = "takecare-gateway-api-hook"

// Mode decides what the installer does when Gateway API CRDs already exist.
type Mode string

// Installation modes, the chart value gatewayAPI.install.
const (
	// ModeAuto installs the CRDs that are missing, leaves the ones at the
	// bundle's version or newer alone, and fails on older ones.
	ModeAuto Mode = "auto"
	// ModeAlways applies the bundle whatever is there, taking ownership.
	ModeAlways Mode = "always"
	// ModeNever does nothing: the CRDs are the customer's business.
	ModeNever Mode = "never"
)

// ParseMode validates a mode string.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ModeAuto, ModeAlways, ModeNever:
		return m, nil
	default:
		return "", fmt.Errorf("unknown mode %q: want auto, always or never", s)
	}
}

// Bundle is the parsed embedded release asset.
type Bundle struct {
	Version string
	Channel string
	CRDs    []*apiextensionsv1.CustomResourceDefinition
}

// Raw returns the embedded asset as shipped upstream, for checksum tests.
func Raw() []byte { return standardInstall }

// Load parses the embedded bundle. Only its CRDs are kept: the
// ValidatingAdmissionPolicy upstream ships next to them guards against
// downgrades, which this package checks itself before applying anything.
func Load() (*Bundle, error) {
	b := &Bundle{}
	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(standardInstall), 4096)
	for {
		var crd apiextensionsv1.CustomResourceDefinition
		err := dec.Decode(&crd)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode embedded bundle: %w", err)
		}
		if crd.Kind != "CustomResourceDefinition" {
			continue
		}
		v, ch := crd.Annotations[AnnotationBundleVersion], crd.Annotations[AnnotationChannel]
		if v == "" || ch == "" {
			return nil, fmt.Errorf("embedded CRD %s carries no bundle version or channel", crd.Name)
		}
		if b.Version == "" {
			b.Version, b.Channel = v, ch
		} else if v != b.Version || ch != b.Channel {
			return nil, fmt.Errorf("embedded CRD %s is %s/%s, the bundle is %s/%s", crd.Name, ch, v, b.Channel, b.Version)
		}
		b.CRDs = append(b.CRDs, &crd)
	}
	if len(b.CRDs) == 0 {
		return nil, errors.New("embedded bundle holds no CRD")
	}
	return b, nil
}

// Existing describes a Gateway API CRD found on the cluster.
type Existing struct {
	Name    string
	Version string // empty when the CRD was not installed from an upstream bundle
	Channel string
}

// Plan is what the installer decided to apply.
type Plan struct {
	Apply []string // CRD names to apply
	Keep  []string // CRD names left as they are
}

// Decide applies the mode's rules to what exists. It is pure, so that the
// rules are testable without a cluster.
func Decide(mode Mode, b *Bundle, existing []Existing) (Plan, error) {
	var p Plan
	switch mode {
	case ModeNever:
		for _, crd := range b.CRDs {
			p.Keep = append(p.Keep, crd.Name)
		}
		return p, nil
	case ModeAlways:
		for _, crd := range b.CRDs {
			p.Apply = append(p.Apply, crd.Name)
		}
		return p, nil
	case ModeAuto:
	default:
		return p, fmt.Errorf("unknown mode %q", mode)
	}
	want, err := version.ParseSemantic(b.Version)
	if err != nil {
		return p, fmt.Errorf("bundle version %q: %w", b.Version, err)
	}
	byName := map[string]Existing{}
	for _, e := range existing {
		byName[e.Name] = e
	}
	var older, unknown []string
	for _, crd := range b.CRDs {
		e, found := byName[crd.Name]
		switch {
		case !found:
			p.Apply = append(p.Apply, crd.Name)
		case e.Version == "":
			unknown = append(unknown, crd.Name)
		default:
			have, err := version.ParseSemantic(e.Version)
			if err != nil {
				unknown = append(unknown, fmt.Sprintf("%s (%s)", crd.Name, e.Version))
				continue
			}
			if have.LessThan(want) {
				older = append(older, fmt.Sprintf("%s (%s/%s)", crd.Name, e.Channel, e.Version))
			} else {
				p.Keep = append(p.Keep, crd.Name)
			}
		}
	}
	if len(older) > 0 || len(unknown) > 0 {
		var msg []string
		if len(older) > 0 {
			msg = append(msg, fmt.Sprintf("older than the bundle %s: %s", b.Version, strings.Join(older, ", ")))
		}
		if len(unknown) > 0 {
			msg = append(msg, fmt.Sprintf("not from an upstream bundle: %s", strings.Join(unknown, ", ")))
		}
		return p, fmt.Errorf("gateway API CRDs already on the cluster are %s; upgrade them yourself, or set gatewayAPI.install=always to let the hook take them over, or gatewayAPI.install=never to keep them", strings.Join(msg, "; "))
	}
	return p, nil
}

// Install applies the embedded bundle according to the mode and waits for the
// applied CRDs to be established.
func Install(ctx context.Context, c client.Client, mode Mode) error {
	logger := log.FromContext(ctx)
	b, err := Load()
	if err != nil {
		return err
	}
	var existing []Existing
	for _, crd := range b.CRDs {
		var got apiextensionsv1.CustomResourceDefinition
		err := c.Get(ctx, client.ObjectKey{Name: crd.Name}, &got)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("get CRD %s: %w", crd.Name, err)
		}
		existing = append(existing, Existing{
			Name:    got.Name,
			Version: got.Annotations[AnnotationBundleVersion],
			Channel: got.Annotations[AnnotationChannel],
		})
	}
	plan, err := Decide(mode, b, existing)
	if err != nil {
		return err
	}
	logger.Info("gateway API CRDs", "bundle", b.Version, "channel", b.Channel, "mode", string(mode), "apply", len(plan.Apply), "keep", len(plan.Keep))
	for _, crd := range b.CRDs {
		if !slices.Contains(plan.Apply, crd.Name) {
			continue
		}
		obj := crd.DeepCopy()
		obj.APIVersion, obj.Kind = apiextensionsv1.SchemeGroupVersion.String(), "CustomResourceDefinition"
		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			return fmt.Errorf("convert CRD %s: %w", crd.Name, err)
		}
		u := &unstructured.Unstructured{Object: content}
		if err := c.Apply(ctx, client.ApplyConfigurationFromUnstructured(u), client.FieldOwner(fieldOwner), client.ForceOwnership); err != nil {
			return fmt.Errorf("apply CRD %s: %w", crd.Name, err)
		}
	}
	for _, name := range plan.Apply {
		if err := waitEstablished(ctx, c, name); err != nil {
			return err
		}
	}
	return nil
}

func waitEstablished(ctx context.Context, c client.Client, name string) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		var crd apiextensionsv1.CustomResourceDefinition
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &crd); err != nil {
			return false, fmt.Errorf("get CRD %s: %w", name, err)
		}
		for _, cond := range crd.Status.Conditions {
			if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
}
