package fieldgen

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every compiler check builds only: a model package's init must never run.
func fixture(t *testing.T, source string) (Options, string) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.test/models\n\ngo 1.25.0\n\nrequire github.com/gabisonia/go-vectorstore v0.0.0\nreplace github.com/gabisonia/go-vectorstore => "+filepath.ToSlash(root)+"\n")
	write(t, dir, "model.go", source)
	return Options{Dir: dir, Model: "Article", Output: "fields.gen.go"}, dir
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func compile(t *testing.T, dir string, wantError string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-c", "-o", filepath.Join(dir, "fixture.test"), ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if wantError == "" {
		if err != nil {
			t.Fatalf("compile: %v\n%s", err, out)
		}
	} else if err == nil || !strings.Contains(string(out), wantError) {
		t.Fatalf("compile error should contain %q: %v\n%s", wantError, err, out)
	}
}

const basicModel = "package models\n" +
	"type Article struct { ID string `vectordata:\"key\"`; Title string `vectordata:\"data,name=stored_title\"`; Rating *int `vectordata:\"data\"`; Enabled bool `vectordata:\"data\"`; Tags []string `vectordata:\"data\"`; Extra map[string]any `vectordata:\"data\"`; Vector []float32 `vectordata:\"vector,dimensions=2\"` }\n" +
	"func init() { panic(\"generation must not run init\") }\n"

func TestCompileContractAndRegeneration(t *testing.T) {
	opts, dir := fixture(t, basicModel)
	// Bootstrap even when application code already references missing output.
	write(t, dir, "use.go", "package models\nvar _ = ArticleFields.Title.Eq(\"hello\")\n")
	if err := Run(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	compile(t, dir, "")
	for _, tc := range []struct{ expression, message string }{
		{`ArticleFields.Titel.Eq("hello")`, "Titel undefined"},
		{`ArticleFields.Title.Eq(123)`, "cannot use 123"},
		{`ArticleFields.Rating.Gt("three")`, `cannot use "three"`},
		{`ArticleFields.Tags.Contains(3)`, "cannot use 3"},
		{`ArticleFields.Enabled.Gt(true)`, "Gt undefined"},
		{`ArticleFields.Tags.Asc()`, "Asc undefined"},
		{`ArticleFields.Extra.Asc()`, "Asc undefined"},
		{`ArticleFields.Vector.Eq(nil)`, "Vector undefined"},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			write(t, dir, "use.go", "package models\nvar _ = "+tc.expression+"\n")
			compile(t, dir, tc.message)
		})
	}
	write(t, dir, "use.go", "package models\n")
	write(t, dir, "model.go", strings.Replace(basicModel, "Title string", "Title int64", 1))
	compile(t, dir, "cannot use model.Title")
	if err := Run(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	compile(t, dir, "")
	write(t, dir, "model.go", strings.Replace(basicModel, "Title string", "Heading string", 1))
	compile(t, dir, "model.Title undefined")
	if err := Run(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	compile(t, dir, "")
}

func TestFreshnessAndFileOwnership(t *testing.T) {
	opts, dir := fixture(t, basicModel)
	if err := Run(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, opts.Output)
	before, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if err := Run(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	opts.Check = true
	if err := Run(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !info.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged generation rewrote the output")
	}
	// Same Go type, different tag semantics: compiler anchors cannot detect this.
	write(t, dir, "model.go", strings.Replace(basicModel, `data,name=stored_title`, `data,type=json`, 1))
	if err := Run(t.Context(), opts); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("freshness: %v", err)
	}
	unchanged, _ := os.ReadFile(path)
	if !bytes.Equal(before, unchanged) {
		t.Fatal("check modified stale output")
	}
	opts.Check = false
	write(t, dir, opts.Output, "package models\n// handwritten\n")
	if err := Run(t.Context(), opts); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("ownership: %v", err)
	}
}

func TestImportedTypesAliasesAndPromotedFields(t *testing.T) {
	source := "package models\nimport (\"time\"; vd \"github.com/gabisonia/go-vectorstore/vectordata\")\n" +
		"type Label string\ntype Base struct { ID uint64 `vectordata:\"key\"`; When time.Time `vectordata:\"data\"`; Hidden string `vectordata:\"data\"` }\n" +
		"type Article struct { *Base; Hidden int `vectordata:\"data\"`; Tags []Label `vectordata:\"data\"`; Days []vd.DateOnly `vectordata:\"data,type=date_only_array\"`; Price vd.Decimal `vectordata:\"data,type=decimal\"`; Maybe **string `vectordata:\"data\"` }\n" +
		"var vsdata = 1\n"
	opts, dir := fixture(t, source)
	opts.Name = "Fields"
	if err := Run(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "use.go", "package models\nimport \"time\"\nvar _ = Fields.When.Gt(time.Now())\nvar _ = Fields.Hidden.Gte(2)\nvar _ = Fields.Tags.Contains(Label(\"x\"))\nvar _ = Fields.Maybe.Eq(\"x\")\n")
	compile(t, dir, "")
	contents, _ := os.ReadFile(filepath.Join(dir, opts.Output))
	if !bytes.Contains(contents, []byte(`Column("When")`)) || !bytes.Contains(contents, []byte("string(value)")) {
		t.Fatalf("missing logical references/alias normalization: %s", contents)
	}
	// An alias to a concrete generic instantiation can be generated too.
	write(t, dir, "model.go", "package models\ntype Row[T any] struct { ID string `vectordata:\"key\"`; Value T `vectordata:\"data\"` }\ntype Article = Row[int]\n")
	write(t, dir, "use.go", "package models\nvar _ = Fields.Value.Gt(3)\n")
	if err := Run(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	compile(t, dir, "")
}

func TestInvalidModels(t *testing.T) {
	for _, tc := range []struct{ source, message string }{
		{"package models\ntype Article struct{}", "exactly one"},
		{"package models\ntype Article int", "must be a struct"},
		{"package models\ntype Article[T any] struct{}", "generic models"},
		{basicModel + "\ntype string = int\n", "shadows predeclared"},
		{strings.Replace(basicModel, "Title string", "Title MissingType", 1), "cannot resolve Go type"},
		{strings.Replace(basicModel, "Tags []string `vectordata:\"data\"`", "Tags int `vectordata:\"data,type=int64_array\"`", 1), "requires a slice"},
	} {
		opts, _ := fixture(t, tc.source)
		err := Run(t.Context(), opts)
		if err == nil || !strings.Contains(err.Error(), tc.message) {
			t.Fatalf("want %q: %v", tc.message, err)
		}
	}
}

func TestInvalidOptions(t *testing.T) {
	for _, opts := range []Options{
		{Model: ""}, {Model: "*Article"}, {Model: "Article", Name: "string"},
		{Model: "Article", Name: "nil"}, {Model: "Article", Name: "for"},
		{Model: "Article", Output: "../outside.go"},
		{Model: "Article", Output: "model_test.go"},
		{Model: "Article", Output: ".hidden.go"},
	} {
		if err := Run(t.Context(), opts); err == nil {
			t.Fatalf("accepted invalid options: %+v", opts)
		}
	}
}

func TestUnnameablePromotedTypeDoesNotPublishOutput(t *testing.T) {
	opts, dir := fixture(t, "package models\nimport \"example.test/models/foreign\"\ntype Article struct { foreign.Base }\n")
	if err := os.Mkdir(filepath.Join(dir, "foreign"), 0755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "foreign"), "model.go", "package foreign\ntype hidden string\ntype Base struct { ID string `vectordata:\"key\"`; Value hidden `vectordata:\"data\"` }\n")
	if err := Run(t.Context(), opts); err == nil {
		t.Fatal("generated code naming an inaccessible foreign type")
	}
	if _, err := os.Stat(filepath.Join(dir, opts.Output)); !os.IsNotExist(err) {
		t.Fatalf("failed generation published output: %v", err)
	}
}
