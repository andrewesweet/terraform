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

// TestScopeFunctions checks that a scope's function table is the same, in its
// set of names and in how those functions behave, for every combination of
// Scope settings that Functions takes into account.
//
// Functions derives most of its table from a table shared by all scopes with
// the same base directory, so this test is what shows that the sharing does not
// leak any part of one scope's configuration into another: for each
// combination it compares the scope's table against the same table derived from
// scratch by referenceFunctions.
func TestScopeFunctions(t *testing.T) {
	// Two directories with files of the same names but different content, so
	// that a table bound to the wrong base directory gives a different result
	// rather than an error.
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

	// The six settings below are the ones beyond the base directory that
	// Functions reads from the scope.
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

// TestScopeFunctions_distinctTablesPerScope checks that each scope still gets
// its own function table. Callers are allowed to modify the returned map, so
// sharing one table between scopes would let one caller affect another.
func TestScopeFunctions_distinctTablesPerScope(t *testing.T) {
	dir := writeFunctionTestFiles(t, "shared")

	first := &Scope{BaseDir: dir, ConsoleMode: true}
	firstFuncs := first.Functions()

	// Modify the first scope's table in the two ways a caller can: remove an
	// entry, and replace an entry that came from the shared base table.
	delete(firstFuncs, "file")
	delete(firstFuncs, "core::file")
	firstFuncs["upper"] = stdlib.LowerFunc
	firstFuncs["core::upper"] = stdlib.LowerFunc

	// Other scopes, with the same and with a different configuration, must not
	// see either change. If the base table were handed out directly, or if
	// one scope's copy were shared with another, these would observe it.
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
			// The "." scope doesn't have this test's files, so there is
			// nothing to compare beyond the function itself being present.
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

// TestScopeFunctions_baseDir checks that the parts of the table that depend on
// a scope's base directory are not shared with scopes using a different one.
func TestScopeFunctions_baseDir(t *testing.T) {
	dirA := writeFunctionTestFiles(t, "alpha")
	dirB := writeFunctionTestFiles(t, "beta")

	scopeA := &Scope{BaseDir: dirA}
	scopeB := &Scope{BaseDir: dirB}

	// Build the tables in an order that would let a table built for one base
	// directory be reused for the other if the cache were keyed incorrectly.
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

// TestScopeFunctions_filesystemResultsPerScope checks that each scope's
// filesystem functions check their results against that scope's own prior
// results, rather than sharing the no-op wrappers used when there is nothing
// to check against.
func TestScopeFunctions_filesystemResultsPerScope(t *testing.T) {
	dir := writeFunctionTestFiles(t, "first")
	path := filepath.Join(dir, "content.txt")

	results := NewFunctionResultsTable(nil)

	// The first call through the checking wrapper records the result.
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

	// Changing the file changes what the function would return, which the
	// wrapper must reject.
	if err := os.WriteFile(path, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"file", "core::file"} {
		_, diags := evalFunctionExpression(&Scope{BaseDir: dir, FunctionResults: results}, name+`("content.txt")`)
		if !diags.HasErrors() {
			t.Fatalf("%s returned an inconsistent result without an error; want an error", name)
		}
	}

	// A provider configuration tolerates inconsistent results, and so does a
	// scope with no prior results to check against.
	for _, name := range []string{"file", "core::file"} {
		if _, diags := evalFunctionExpression(&Scope{BaseDir: dir, FunctionResults: results, ForProvider: true}, name+`("content.txt")`); diags.HasErrors() {
			t.Errorf("unexpected error from %s for a provider configuration: %s", name, diags.Error())
		}
		if _, diags := evalFunctionExpression(&Scope{BaseDir: dir}, name+`("content.txt")`); diags.HasErrors() {
			t.Errorf("unexpected error from %s without prior results: %s", name, diags.Error())
		}
	}
}

// TestScopeFunctions_templateFunctionsSeeScopeFunctions checks that the
// template-rendering functions call the functions of the scope they are
// rendering for, including any functions contributed by providers.
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

// TestScopeFunctions_concurrentScopes checks that building the function tables
// concurrent graph walks need is safe, and that concurrent scopes with
// different configurations do not see each other's functions.
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

// BenchmarkScopeFunctions measures building the function table for a new
// scope, which is what happens for the first expression evaluated in each
// scope the evaluator creates.
func BenchmarkScopeFunctions(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		scope := &Scope{BaseDir: "."}
		if len(scope.Functions()) == 0 {
			b.Fatal("no functions in table")
		}
	}
}

// referenceFunctions derives the function table for the given scope by
// building every part of it from scratch, which is what Scope.Functions did
// before the parts that do not depend on the rest of the scope's configuration
// were cached in a table shared by all scopes with the same base directory.
//
// It is the reference that the cached implementation must reproduce, so that
// the cache can be shown not to change any scope's set of functions or their
// behaviour.
func referenceFunctions(s *Scope) map[string]function.Function {
	immutableResults := immutableResults
	if s.ForProvider {
		immutableResults = filesystemNoopWrapper
	}

	coreFuncs := baseFunctions(s.BaseDir)

	// Modify the functions which behave slightly differently in core.
	// Filesystem functions will check for consistent results, and the
	// template functions need to be updated to close over the correct
	// versions of the filesystem functions.
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

	// Our two template-rendering functions want to be able to call
	// all of the other functions themselves, but we pass them indirectly
	// via a callback to avoid chicken/egg problems while initializing
	// the functions table.
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

// checkFunctionTablesEqual checks that two function tables contain the same
// names, described the same way, accepting and returning the same types.
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

// returnType describes the type the given function returns when its arguments
// have the types it declares, along with any error. The result is a string so
// that a function whose return type is invalid compares equal to itself.
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
		// A function whose own Type implementation panics gets an error that
		// includes a stack trace, which will never match between two calls; the
		// first line is the part that describes the failure.
		msg, _, _ := strings.Cut(err.Error(), "\n")
		return "error: " + msg
	}
	return retType.GoString()
}

// functionProbes are expressions that exercise the parts of a function table
// that vary between scopes.
var functionProbes = []struct {
	src string
	// knownOnly is for the impure functions, whose result cannot be compared
	// between two calls; for those we only compare the type of the result and
	// whether it is known, which is what changes when a scope wraps them.
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

// checkFunctionProbesEqual checks that the functions in two tables behave the
// same way when called.
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
			// Some results are capsule values that do not compare as equal to
			// values of an identical shape, so compare their representations.
			if got, want := gotVal.GoString(), wantVal.GoString(); got != want {
				t.Fatalf("wrong result\ngot:  %s\nwant: %s", got, want)
			}
		})
	}
}

// evalFunctionExpression evaluates the given expression in the given scope.
func evalFunctionExpression(scope *Scope, src string) (cty.Value, hcl.Diagnostics) {
	return evalFunctionExpressionTable(scope.Functions(), src)
}

// evalFunctionExpressionTable evaluates the given expression using only the
// functions in the given table as its evaluation context.
func evalFunctionExpressionTable(table map[string]function.Function, src string) (cty.Value, hcl.Diagnostics) {
	expr, diags := hclsyntax.ParseExpression([]byte(src), "probe.hcl", hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		// The probes are constants in this file, so a parse error is a bug in
		// the test rather than a difference between two tables.
		return cty.NilVal, diags
	}

	return expr.Value(&hcl.EvalContext{Functions: table})
}

// writeFunctionTestFiles writes the files that the probes read into a new
// temporary directory, and returns the directory.
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
