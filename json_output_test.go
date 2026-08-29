package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type wireField struct {
	Path string
	Type string
	Tag  string
}

func TestJSONOutputSchemaMatchesK6Internal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		local        reflect.Type
		upstreamType string
	}{
		{name: "metric", local: reflect.TypeFor[metricEnvelope](), upstreamType: "metricEnvelope"},
		{name: "point", local: reflect.TypeFor[pointEnvelope](), upstreamType: "sampleEnvelope"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			local := localWireSchema(test.local)
			upstream := k6WireSchema(t, test.upstreamType)
			if reflect.DeepEqual(local, upstream) {
				return
			}

			localJSON, err := json.MarshalIndent(local, "", "  ")
			if err != nil {
				t.Fatalf("marshal local schema: %v", err)
			}
			upstreamJSON, err := json.MarshalIndent(upstream, "", "  ")
			if err != nil {
				t.Fatalf("marshal k6 schema: %v", err)
			}
			t.Fatalf("JSON schema differs from k6 internal output\nlocal:\n%s\nk6:\n%s", localJSON, upstreamJSON)
		})
	}
}

func localWireSchema(root reflect.Type) []wireField {
	fields := make([]wireField, 0)
	collectLocalFields(root, "", root.PkgPath(), &fields)
	sortWireFields(fields)
	return fields
}

func collectLocalFields(current reflect.Type, parentPath, localPackage string, fields *[]wireField) {
	for field := range current.Fields() {
		jsonTag := field.Tag.Get("json")
		jsonName := wireJSONFieldName(jsonTag)
		path := jsonName
		if parentPath != "" {
			path = parentPath + "." + jsonName
		}

		if field.Type.Kind() == reflect.Struct && field.Type.PkgPath() == localPackage {
			collectLocalFields(field.Type, path, localPackage, fields)
			continue
		}
		*fields = append(*fields, wireField{Path: path, Type: reflectTypeName(field.Type), Tag: jsonTag})
	}
}

func reflectTypeName(value reflect.Type) string {
	if value.Name() != "" {
		if value.PkgPath() == "" {
			return value.Name()
		}
		return path.Base(value.PkgPath()) + "." + value.Name()
	}

	switch value.Kind() {
	case reflect.Pointer:
		return "*" + reflectTypeName(value.Elem())
	case reflect.Slice:
		return "[]" + reflectTypeName(value.Elem())
	case reflect.Map:
		return "map[" + reflectTypeName(value.Key()) + "]" + reflectTypeName(value.Elem())
	default:
		return value.String()
	}
}

func k6WireSchema(t *testing.T, typeName string) []wireField {
	t.Helper()

	command := exec.CommandContext(t.Context(), "go", "list", "-m", "-f={{.Dir}}", "go.k6.io/k6")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("locate k6 module: %v", err)
	}
	filename := filepath.Join(strings.TrimSpace(string(output)), "internal", "output", "json", "wrapper.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), filename, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse k6 JSON schema: %v", err)
	}

	var root *ast.StructType
	for _, declaration := range parsed.Decls {
		generic, ok := declaration.(*ast.GenDecl)
		if !ok || generic.Tok != token.TYPE {
			continue
		}
		for _, specification := range generic.Specs {
			typeSpecification, ok := specification.(*ast.TypeSpec)
			if !ok || typeSpecification.Name.Name != typeName {
				continue
			}
			root, ok = typeSpecification.Type.(*ast.StructType)
			if !ok {
				t.Fatalf("k6 type %s is not a struct", typeName)
			}
		}
	}
	if root == nil {
		t.Fatalf("k6 type %s was not found in %s", typeName, filename)
	}

	fields := make([]wireField, 0)
	collectK6Fields(t, root, "", &fields)
	sortWireFields(fields)
	return fields
}

func collectK6Fields(t *testing.T, current *ast.StructType, parentPath string, fields *[]wireField) {
	t.Helper()

	for _, field := range current.Fields.List {
		if len(field.Names) != 1 || field.Tag == nil {
			t.Fatalf("unsupported field in k6 JSON schema")
		}
		tag, err := strconv.Unquote(field.Tag.Value)
		if err != nil {
			t.Fatalf("decode k6 struct tag: %v", err)
		}
		jsonTag := reflect.StructTag(tag).Get("json")
		jsonName := wireJSONFieldName(jsonTag)
		path := jsonName
		if parentPath != "" {
			path = parentPath + "." + jsonName
		}

		if nested, ok := field.Type.(*ast.StructType); ok {
			collectK6Fields(t, nested, path, fields)
			continue
		}
		var rendered bytes.Buffer
		if err := printer.Fprint(&rendered, token.NewFileSet(), field.Type); err != nil {
			t.Fatalf("render k6 field type: %v", err)
		}
		*fields = append(*fields, wireField{Path: path, Type: rendered.String(), Tag: jsonTag})
	}
}

func sortWireFields(fields []wireField) {
	slices.SortFunc(fields, func(left, right wireField) int {
		return cmp.Compare(left.Path, right.Path)
	})
}

func wireJSONFieldName(tag string) string {
	name, _, _ := strings.Cut(tag, ",")
	return name
}
