// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package questionnaire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"
	apiextensionsinternal "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/pruning"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/config/crd"
	"github.com/agaetis-it/takecare-agents/internal/mapserver"
)

// group is the API group whose kinds an output is checked against.
const group = "takecare.agaetis.fr"

// Check runs the checks before writing (spec 13 §4.3) on outputs, to be
// written into dir, and returns every fault at once as Faults:
//   - a .yaml or .yml output parses, every document of it;
//   - the outputs under mapDir (relative to dir), with the Map files
//     already there, pass the Map server's checks;
//   - a document of a takecare.agaetis.fr kind passes that kind's schema
//     and has no field the schema does not know, as the API server checks
//     it with strict field validation.
func Check(dir, mapDir string, outputs []Rendered) error {
	if !filepath.IsLocal(filepath.FromSlash(mapDir)) {
		return fmt.Errorf("the Map directory %q is not inside the repository", mapDir)
	}
	var (
		faults   Faults
		mapFiles map[string][]byte
	)
	for _, o := range outputs {
		if rel, ok := under(o.Path, mapDir); ok {
			if mapFiles == nil {
				existing, err := readMap(dir, mapDir)
				if err != nil {
					return err
				}
				mapFiles = existing
			}
			mapFiles[path.Join("map", rel)] = o.Data
		}
		if ext := path.Ext(o.Path); ext != extYAML && ext != extYML {
			continue
		}
		docs, err := documents(o.Data)
		if err != nil {
			faults = append(faults, Fault{o.Path, fmt.Sprintf("not YAML: %v", err)})
			continue
		}
		for i, doc := range docs {
			for _, msg := range checkKind(doc) {
				faults = append(faults, Fault{o.Path, fmt.Sprintf("document %d: %s", i+1, msg)})
			}
		}
	}
	if mapFiles != nil {
		_, errs := mapserver.Check(mapFiles)
		for _, err := range errs {
			faults = append(faults, Fault{mapDir, err.Error()})
		}
	}
	if len(faults) > 0 {
		return faults
	}
	return nil
}

// under tells whether p sits under dir, and where.
func under(p, dir string) (string, bool) {
	dir = path.Clean(dir)
	if dir == "." {
		return p, true
	}
	rel, ok := strings.CutPrefix(p, dir+"/")
	return rel, ok
}

// readMap reads the Map files already in dir/mapDir, as the Map server
// does (dotfiles left out), keyed by path under map/. A missing directory
// is an empty Map.
func readMap(dir, mapDir string) (map[string][]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open the repository directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	files := map[string][]byte{}
	sub, err := fs.Sub(root.FS(), path.Clean(mapDir))
	if err != nil {
		return nil, fmt.Errorf("read the Map directory: %w", err)
	}
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case p != "." && strings.HasPrefix(d.Name(), "."):
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		case d.IsDir():
			return nil
		}
		raw, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		files[path.Join("map", p)] = raw
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the Map directory %s: %w", mapDir, err)
	}
	return files, nil
}

// documents parses every YAML document of raw into JSON-shaped values.
func documents(raw []byte) ([]any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var out []any
	for {
		var node yaml.Node
		err := dec.Decode(&node)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		doc, err := yaml.Marshal(&node)
		if err != nil {
			return nil, err
		}
		js, err := sigsyaml.YAMLToJSON(doc)
		if err != nil {
			return nil, err
		}
		var v any
		if err := json.Unmarshal(js, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
}

// checkKind checks doc against its kind's schema when it is one of ours.
func checkKind(doc any) []string {
	obj, ok := doc.(map[string]any)
	if !ok {
		return nil
	}
	apiVersion, _ := obj["apiVersion"].(string)
	g, version, ok := strings.Cut(apiVersion, "/")
	if !ok || g != group {
		return nil
	}
	kind, _ := obj["kind"].(string)
	schemas, err := kindSchemas()
	if err != nil {
		return []string{err.Error()}
	}
	s, ok := schemas[kind+"/"+version]
	if !ok {
		return []string{fmt.Sprintf("%s %s is not a served kind of %s", kind, version, group)}
	}
	var msgs []string
	meta, _ := obj["metadata"].(map[string]any)
	if name, _ := meta["name"].(string); name == "" {
		if gen, _ := meta["generateName"].(string); gen == "" {
			msgs = append(msgs, fmt.Sprintf("%s has no metadata.name", kind))
		}
	}
	for _, e := range validation.ValidateCustomResource(nil, obj, s.validator) {
		msgs = append(msgs, fmt.Sprintf("%s: %s", kind, e.Error()))
	}
	// Pruning changes what it walks: walk a copy.
	var dup any
	raw, _ := json.Marshal(obj)
	_ = json.Unmarshal(raw, &dup)
	unknown := pruning.PruneWithOptions(dup, s.structural, true, structuralschema.UnknownFieldPathOptions{TrackUnknownFieldPaths: true})
	for _, f := range unknown {
		msgs = append(msgs, fmt.Sprintf("%s: unknown field %q", kind, f))
	}
	return msgs
}

// kindSchema is one served version of one of our kinds.
type kindSchema struct {
	validator  validation.SchemaValidator
	structural *structuralschema.Structural
}

// kindSchemas reads the embedded CRDs once, keyed by kind/version.
var kindSchemas = sync.OnceValues(func() (map[string]kindSchema, error) {
	out := map[string]kindSchema{}
	err := fs.WalkDir(crd.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(crd.FS, p)
		if err != nil {
			return err
		}
		var def apiextensionsv1.CustomResourceDefinition
		if err := sigsyaml.UnmarshalStrict(raw, &def); err != nil {
			return fmt.Errorf("the embedded CRD %s: %w", p, err)
		}
		for _, v := range def.Spec.Versions {
			if !v.Served || v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
				continue
			}
			var internal apiextensionsinternal.JSONSchemaProps
			if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(v.Schema.OpenAPIV3Schema, &internal, nil); err != nil {
				return fmt.Errorf("the embedded CRD %s %s: %w", p, v.Name, err)
			}
			validator, _, err := validation.NewSchemaValidator(&internal)
			if err != nil {
				return fmt.Errorf("the embedded CRD %s %s: %w", p, v.Name, err)
			}
			structural, err := structuralschema.NewStructural(&internal)
			if err != nil {
				return fmt.Errorf("the embedded CRD %s %s: %w", p, v.Name, err)
			}
			out[def.Spec.Names.Kind+"/"+v.Name] = kindSchema{validator: validator, structural: structural}
		}
		return nil
	})
	return out, err
})
