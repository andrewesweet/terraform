// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package lang

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"

	"github.com/hashicorp/terraform/internal/collections"
	"github.com/hashicorp/terraform/internal/lang/funcs"
)

func TestScopeFunctions(t *testing.T) {
	dirA := writeFunctionTestFiles(t, "alpha")
	dirB := writeFunctionTestFiles(t, "beta")

	externalFuncs := ExternalFuncs{
		Provider: map[string]map[string]function.Function{
			"foo": {
				"upper": stdlib.UpperFunc,
			},
		},
	}
	planTimestamp := time.Date(2020, 6, 1, 12, 0, 0, 0, time.UTC)

	baseDirs := []struct {
		name string
		dir  string
	}{
		{"testdata", "./testdata/functions-test"},
		{"base directory A", dirA},
		{"base directory B", dirB},
		{"working directory", "."},
	}

	const (
		forProvider = 1 << iota
		consoleMode
		pureOnly
		setPlanTimestamp
		withFunctionResults
		withExternalFuncs
	)

	for _, baseDir := range baseDirs {
		for settings := 0; settings < 1<<6; settings++ {
			scope := &Scope{BaseDir: baseDir.dir}
			name := baseDir.name
			if settings&forProvider != 0 {
				scope.ForProvider = true
				name += "/for-provider"
			}
			if settings&consoleMode != 0 {
				scope.ConsoleMode = true
				name += "/console"
			}
			if settings&pureOnly != 0 {
				scope.PureOnly = true
				name += "/pure-only"
			}
			if settings&setPlanTimestamp != 0 {
				scope.PlanTimestamp = planTimestamp
				name += "/plan-timestamp"
			}
			if settings&withFunctionResults != 0 {
				scope.FunctionResults = NewFunctionResultsTable(nil)
				name += "/function-results"
			}
			if settings&withExternalFuncs != 0 {
				scope.ExternalFuncs = externalFuncs
				name += "/external-funcs"
			}

			t.Run(name, func(t *testing.T) {
				got := scope.Functions()
				want := referenceFunctions(scope)

				checkFunctionTablesEqual(t, got, want)
				checkFunctionProbesEqual(t, got, want)
			})
		}
	}
}

func TestScopeFunctions_distinctTablesPerScope(t *testing.T) {
	dir := writeFunctionTestFiles(t, "shared")

	first := &Scope{BaseDir: dir, ConsoleMode: true}
	firstFuncs := first.Functions()

	delete(firstFuncs, "file")
	delete(firstFuncs, "core::file")
	firstFuncs["upper"] = stdlib.LowerFunc
	firstFuncs["core::upper"] = stdlib.LowerFunc

	for _, scope := range []*Scope{
		{BaseDir: dir, ConsoleMode: true},
		{BaseDir: dir},
		{BaseDir: "."},
	} {
		got, diags := evalFunctionExpression(scope, `upper("Hello")`)
		if diags.HasErrors() {
			t.Fatalf("unexpected error: %s", diags.Error())
		}
		if want := cty.StringVal("HELLO"); !got.RawEquals(want) {
			t.Errorf("wrong result for a scope constructed after one scope's table was modified: got %#v, want %#v", got, want)
		}

		if scope.BaseDir != dir {
			continue
		}

		got, diags = evalFunctionExpression(scope, `file("content.txt")`)
		if diags.HasErrors() {
			t.Fatalf("unexpected error: %s", diags.Error())
		}
		if want := cty.StringVal("shared"); !got.RawEquals(want) {
			t.Errorf("wrong result for a scope constructed after one scope's table was modified: got %#v, want %#v", got, want)
		}
	}
}

func TestScopeFunctions_baseDir(t *testing.T) {
	dirA := writeFunctionTestFiles(t, "alpha")
	dirB := writeFunctionTestFiles(t, "beta")

	scopeA := &Scope{BaseDir: dirA}
	scopeB := &Scope{BaseDir: dirB}

	for _, test := range []struct {
		scope *Scope
		want  string
	}{
		{scopeB, "beta"},
		{scopeA, "alpha"},
		{scopeB, "beta"},
	} {
		for _, name := range []string{"file", "core::file"} {
			got, diags := evalFunctionExpression(test.scope, name+`("content.txt")`)
			if diags.HasErrors() {
				t.Fatalf("unexpected error: %s", diags.Error())
			}
			if want := cty.StringVal(test.want); !got.RawEquals(want) {
				t.Errorf("wrong result from %s for base directory %q: got %#v, want %#v", name, test.scope.BaseDir, got, want)
			}
		}
	}
}

func TestScopeFunctions_filesystemResultsPerScope(t *testing.T) {
	dir := writeFunctionTestFiles(t, "first")
	path := filepath.Join(dir, "content.txt")

	results := NewFunctionResultsTable(nil)

	for _, name := range []string{"file", "core::file"} {
		scope := &Scope{BaseDir: dir, FunctionResults: results}
		got, diags := evalFunctionExpression(scope, name+`("content.txt")`)
		if diags.HasErrors() {
			t.Fatalf("unexpected error from %s: %s", name, diags.Error())
		}
		if want := cty.StringVal("first"); !got.RawEquals(want) {
			t.Fatalf("wrong result from %s: got %#v, want %#v", name, got, want)
		}
	}

	if err := os.WriteFile(path, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"file", "core::file"} {
		_, diags := evalFunctionExpression(&Scope{BaseDir: dir, FunctionResults: results}, name+`("content.txt")`)
		if !diags.HasErrors() {
			t.Fatalf("%s returned an inconsistent result without an error; want an error", name)
		}
	}

	for _, name := range []string{"file", "core::file"} {
		if _, diags := evalFunctionExpression(&Scope{BaseDir: dir, FunctionResults: results, ForProvider: true}, name+`("content.txt")`); diags.HasErrors() {
			t.Errorf("unexpected error from %s for a provider configuration: %s", name, diags.Error())
		}
		if _, diags := evalFunctionExpression(&Scope{BaseDir: dir}, name+`("content.txt")`); diags.HasErrors() {
			t.Errorf("unexpected error from %s without prior results: %s", name, diags.Error())
		}
	}
}

func TestScopeFunctions_templateFunctionsSeeScopeFunctions(t *testing.T) {
	dir := writeFunctionTestFiles(t, "alpha")

	scope := &Scope{
		BaseDir: dir,
		ExternalFuncs: ExternalFuncs{
			Provider: map[string]map[string]function.Function{
				"foo": {
					"upper": stdlib.UpperFunc,
				},
			},
		},
	}

	for _, name := range []string{"templatefile", "core::templatefile"} {
		got, diags := evalFunctionExpression(scope, name+`("template.tmpl", {name = "unused"})`)
		if diags.HasErrors() {
			t.Fatalf("unexpected error: %s", diags.Error())
		}
		if want := cty.StringVal("UNUSED"); !got.RawEquals(want) {
			t.Errorf("wrong result for %s: got %#v, want %#v", name, got, want)
		}
	}
}

func TestScopeFunctions_concurrentScopes(t *testing.T) {
	const testBaseDir = "./testdata/functions-test"

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				scope := &Scope{
					BaseDir:     testBaseDir,
					ConsoleMode: i%2 == 0,
					PureOnly:    j%2 == 0,
					ForProvider: j%3 == 0,
				}
				table := scope.Functions()

				if _, ok := table["type"]; ok != scope.ConsoleMode {
					t.Errorf("console mode %t: presence of \"type\" is wrong", scope.ConsoleMode)
				}
				if _, ok := table["file"]; !ok {
					t.Errorf("missing \"file\" function")
				}

				got, diags := evalFunctionExpression(scope, `file("hello.txt")`)
				if diags.HasErrors() {
					t.Errorf("unexpected error: %s", diags.Error())
					continue
				}
				if want := cty.StringVal("hello!"); !got.RawEquals(want) {
					t.Errorf("wrong result: got %#v, want %#v", got, want)
				}
			}
		}(i)
	}
	wg.Wait()
}

func BenchmarkScopeFunctions(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		scope := &Scope{BaseDir: "."}
		if len(scope.Functions()) == 0 {
			b.Fatal("no functions in table")
		}
	}
}

func referenceFunctions(s *Scope) map[string]function.Function {
	immutableResults := immutableResults
	if s.ForProvider {
		immutableResults = filesystemNoopWrapper
	}

	coreFuncs := baseFunctions(s.BaseDir)

	coreFuncs["file"] = funcs.MakeFileFunc(s.BaseDir, false, immutableResults("file", s.FunctionResults))
	coreFuncs["fileexists"] = funcs.MakeFileExistsFunc(s.BaseDir, immutableResults("fileexists", s.FunctionResults))
	coreFuncs["fileset"] = funcs.MakeFileSetFunc(s.BaseDir, immutableResults("fileset", s.FunctionResults))
	coreFuncs["filebase64"] = funcs.MakeFileFunc(s.BaseDir, true, immutableResults("filebase64", s.FunctionResults))
	coreFuncs["filebase64sha256"] = funcs.MakeFileBase64Sha256Func(s.BaseDir, immutableResults("filebase64sha256", s.FunctionResults))
	coreFuncs["filebase64sha512"] = funcs.MakeFileBase64Sha512Func(s.BaseDir, immutableResults("filebase64sha512", s.FunctionResults))
	coreFuncs["filemd5"] = funcs.MakeFileMd5Func(s.BaseDir, immutableResults("filemd5", s.FunctionResults))
	coreFuncs["filesha1"] = funcs.MakeFileSha1Func(s.BaseDir, immutableResults("filesha1", s.FunctionResults))
	coreFuncs["filesha256"] = funcs.MakeFileSha256Func(s.BaseDir, immutableResults("filesha256", s.FunctionResults))
	coreFuncs["filesha512"] = funcs.MakeFileSha512Func(s.BaseDir, immutableResults("filesha512", s.FunctionResults))

	ret := make(map[string]function.Function)

	funcsFunc := func() (map[string]function.Function, collections.Set[string], collections.Set[string]) {
		return ret, filesystemFunctions, templateFunctions
	}
	coreFuncs["templatefile"] = funcs.MakeTemplateFileFunc(s.BaseDir, funcsFunc, immutableResults("templatefile", s.FunctionResults))
	coreFuncs["templatestring"] = funcs.MakeTemplateStringFunc(funcsFunc)

	if s.ConsoleMode {
		coreFuncs["type"] = funcs.TypeFunc
	}

	if !s.ConsoleMode {
		coreFuncs["plantimestamp"] = funcs.MakeStaticTimestampFunc(s.PlanTimestamp)
	}

	if s.PureOnly {
		for _, name := range impureFunctions {
			coreFuncs[name] = function.Unpredictable(coreFuncs[name])
		}
	}

	for name, fn := range coreFuncs {
		fn = funcs.WithDescription(name, fn)
		ret[name] = fn
		ret["core::"+name] = fn
	}

	for providerLocalName, providerFuncs := range s.ExternalFuncs.Provider {
		for funcName, fn := range providerFuncs {
			ret["provider::"+providerLocalName+"::"+funcName] = fn
		}
	}

	return ret
}

func checkFunctionTablesEqual(t *testing.T, got, want map[string]function.Function) {
	t.Helper()

	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("missing function %q", name)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected function %q", name)
		}
	}

	for name, wantFn := range want {
		gotFn, ok := got[name]
		if !ok {
			continue
		}
		if got, want := gotFn.Description(), wantFn.Description(); got != want {
			t.Errorf("%q: wrong description %q; want %q", name, got, want)
		}
		if got, want := gotFn.Params(), wantFn.Params(); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: wrong parameters\ngot:  %#v\nwant: %#v", name, got, want)
		}
		if got, want := gotFn.VarParam(), wantFn.VarParam(); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: wrong variadic parameter\ngot:  %#v\nwant: %#v", name, got, want)
		}
		if got, want := returnType(gotFn), returnType(wantFn); got != want {
			t.Errorf("%q: wrong return type %q; want %q", name, got, want)
		}
	}
}

func returnType(fn function.Function) string {
	argTypes := make([]cty.Type, 0, len(fn.Params())+1)
	for _, param := range fn.Params() {
		argTypes = append(argTypes, param.Type)
	}
	if param := fn.VarParam(); param != nil {
		argTypes = append(argTypes, param.Type)
	}

	retType, err := fn.ReturnType(argTypes)
	if err != nil {
		msg, _, _ := strings.Cut(err.Error(), "\n")
		return "error: " + msg
	}
	return retType.GoString()
}

var functionProbes = []struct {
	src       string
	knownOnly bool
}{
	{src: `file("content.txt")`},
	{src: `fileexists("content.txt")`},
	{src: `fileset("subdirectory", "*")`},
	{src: `filebase64("content.txt")`},
	{src: `filebase64sha256("content.txt")`},
	{src: `filebase64sha512("content.txt")`},
	{src: `filemd5("content.txt")`},
	{src: `filesha1("content.txt")`},
	{src: `filesha256("content.txt")`},
	{src: `filesha512("content.txt")`},
	{src: `templatefile("template.tmpl", {name = "unused"})`},
	{src: `templatestring("hello, $${name}!", {name = "world"})`},
	{src: `type(1)`},
	{src: `plantimestamp()`},
	{src: `bcrypt("hello", 4)`, knownOnly: true},
	{src: `timestamp()`, knownOnly: true},
	{src: `uuid()`, knownOnly: true},
	{src: `provider::foo::upper("hello")`},
	{src: `core::file("content.txt")`},
	{src: `core::plantimestamp()`},
	{src: `core::templatefile("template.tmpl", {name = "unused"})`},
	{src: `upper("hello")`},
	{src: `length([1, 2])`},
}

func checkFunctionProbesEqual(t *testing.T, got, want map[string]function.Function) {
	t.Helper()

	for _, probe := range functionProbes {
		t.Run(probe.src, func(t *testing.T) {
			gotVal, gotDiags := evalFunctionExpressionTable(got, probe.src)
			wantVal, wantDiags := evalFunctionExpressionTable(want, probe.src)

			if gotDiags.HasErrors() != wantDiags.HasErrors() {
				t.Fatalf("wrong error\ngot:  %s\nwant: %s", gotDiags.Error(), wantDiags.Error())
			}
			if gotDiags.HasErrors() {
				if got, want := gotDiags.Error(), wantDiags.Error(); got != want {
					t.Fatalf("wrong error\ngot:  %s\nwant: %s", got, want)
				}
				return
			}

			if !gotVal.Type().Equals(wantVal.Type()) {
				t.Fatalf("wrong result type\ngot:  %#v\nwant: %#v", gotVal.Type(), wantVal.Type())
			}
			if probe.knownOnly {
				if gotVal.IsKnown() != wantVal.IsKnown() {
					t.Fatalf("wrong result\ngot:  %#v\nwant: %#v", gotVal, wantVal)
				}
				return
			}
			if got, want := gotVal.GoString(), wantVal.GoString(); got != want {
				t.Fatalf("wrong result\ngot:  %s\nwant: %s", got, want)
			}
		})
	}
}

func evalFunctionExpression(scope *Scope, src string) (cty.Value, hcl.Diagnostics) {
	return evalFunctionExpressionTable(scope.Functions(), src)
}

func evalFunctionExpressionTable(table map[string]function.Function, src string) (cty.Value, hcl.Diagnostics) {
	expr, diags := hclsyntax.ParseExpression([]byte(src), "probe.hcl", hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return cty.NilVal, diags
	}

	return expr.Value(&hcl.EvalContext{Functions: table})
}

func writeFunctionTestFiles(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	files := map[string]string{
		"content.txt":             content,
		"hello.txt":               "hello!",
		"hello.tmpl":              "Hello, ${name}!",
		"template.tmpl":           `${provider::foo::upper(name)}`,
		"subdirectory/hello.txt":  "hello!",
		"subdirectory/hello.tmpl": "Hello, ${name}!",
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}
