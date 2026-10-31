package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"

	archgo "github.com/arch-go/arch-go/api"
	"github.com/arch-go/arch-go/api/configuration"
)

// The A2A inbound adapter (docs/adr/0009-a2a-inbound-adapter.md) is, like the
// MCP one, additive and READ-ONLY: the fleet rule is that an A2A skill may
// change state only if the context's MCP surface already offers that write,
// and this context's MCP charter (ADR 0006) is read-only. The rules below pin
// the shape mechanically.

const (
	a2aAdapterDir  = "../adapters/inbound/a2a"
	a2aCmdDir      = "../../cmd/a2a"
	usecasesImport = "internal/application/usecases"
	a2aClientImp   = "github.com/a2aproject/a2a-go/v2/a2aclient"
)

// TestA2AAdapterDependencyRule: the A2A adapter depends only on the
// application and domain layers (never on an outbound adapter, the REST
// adapter or cmd), and nothing else in internal/ depends on it.
func TestA2AAdapterDependencyRule(t *testing.T) {
	moduleInfo := configuration.Load(modulePath)

	t.Run("a2a adapter depends only on application and domain", func(t *testing.T) {
		result := archgo.CheckArchitecture(moduleInfo, configuration.Config{
			DependenciesRules: []*configuration.DependenciesRule{{
				Package: "**.internal.adapters.inbound.a2a.**",
				ShouldOnlyDependsOn: &configuration.Dependencies{
					Internal: []string{
						"**.internal.adapters.inbound.a2a.**",
						"**.internal.application.**",
						"**.internal.domain.**",
					},
				},
			}},
		})
		assertArchGoPasses(t, result)
	})

	t.Run("nothing else depends on the a2a adapter", func(t *testing.T) {
		result := archgo.CheckArchitecture(moduleInfo, configuration.Config{
			DependenciesRules: []*configuration.DependenciesRule{{
				Package: "**.internal.**",
				ShouldNotDependsOn: &configuration.Dependencies{
					Internal: []string{"**.internal.adapters.inbound.a2a.**"},
				},
			}},
		})
		assertArchGoPasses(t, result)
	})
}

// parseSources parses every .go file in the given sources (path -> content)
// into ASTs keyed by path.
func parseSources(t *testing.T, sources map[string]string) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	files := make(map[string]*ast.File, len(sources))
	for path, src := range sources {
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files[path] = f
	}
	return files
}

// parseDir parses the non-test .go files of dir. Comments are not part of the
// identifiers the sensors inspect, so prose about write use cases is fine.
func parseDir(t *testing.T, dir string) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, path := range goFilesUnder(t, dir, false) {
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files[path] = f
	}
	return files
}

// importedPaths returns the import paths of one file.
func importedPaths(f *ast.File) []string {
	paths := make([]string, 0, len(f.Imports))
	for _, imp := range f.Imports {
		paths = append(paths, strings.Trim(imp.Path.Value, `"`))
	}
	return paths
}

// usecasesIdents returns every identifier X of a `usecases.X` selector in f.
func usecasesIdents(f *ast.File) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == "usecases" {
			out = append(out, sel.Sel.Name)
		}
		return true
	})
	return out
}

// a2aClientViolations lists the files that import the A2A CLIENT package.
// This context hosts an inbound agent only: delegating outward would be a new
// decision with its own ADR.
func a2aClientViolations(files map[string]*ast.File) []string {
	var out []string
	for path, f := range files {
		for _, imp := range importedPaths(f) {
			if imp == a2aClientImp {
				out = append(out, path+": imports the outbound A2A client "+imp)
			}
		}
	}
	sort.Strings(out)
	return out
}

// adapterUseCaseViolations: the adapter reaches the application only through
// its Reads interface, so it must neither import the use-case package nor name
// anything in it.
func adapterUseCaseViolations(files map[string]*ast.File) []string {
	var out []string
	for path, f := range files {
		for _, imp := range importedPaths(f) {
			if strings.HasSuffix(imp, usecasesImport) {
				out = append(out, path+": imports "+imp+" (the adapter holds Reads, never a use case)")
			}
		}
		for _, id := range usecasesIdents(f) {
			out = append(out, path+": names usecases."+id)
		}
	}
	sort.Strings(out)
	return out
}

// cmdUseCaseAllowlist is everything cmd/a2a may take from the use-case
// package: build the shared Service. No named use case or write method.
var cmdUseCaseAllowlist = map[string]bool{"New": true, "Deps": true, "Config": true}

// cmdUseCaseViolations flags any usecases.X in cmd/a2a outside the allowlist.
func cmdUseCaseViolations(files map[string]*ast.File) []string {
	var out []string
	for path, f := range files {
		for _, id := range usecasesIdents(f) {
			if !cmdUseCaseAllowlist[id] {
				out = append(out, path+": names usecases."+id+" (allowed: New, Deps, Config)")
			}
		}
	}
	sort.Strings(out)
	return out
}

// readsMethods returns the sorted method names of the `Reads` interface
// declared in files, or nil when it is not declared.
func readsMethods(files map[string]*ast.File) []string {
	var methods []string
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok || spec.Name.Name != "Reads" {
				return true
			}
			iface, ok := spec.Type.(*ast.InterfaceType)
			if !ok {
				return true
			}
			for _, m := range iface.Methods.List {
				for _, name := range m.Names {
					methods = append(methods, name.Name)
				}
			}
			return true
		})
	}
	sort.Strings(methods)
	return methods
}

var wantA2AReads = []string{"GetPurchaseOrder", "GetScorecard"}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestA2AAdapterHasNoOutboundA2AClient: this context only hosts an agent. The
// two contexts that ban sibling calls are not this one, but the rule is the
// same for the whole rollout: no outbound delegation without an ADR.
func TestA2AAdapterHasNoOutboundA2AClient(t *testing.T) {
	files := parseDir(t, "../")
	for path, f := range parseDir(t, a2aCmdDir) {
		files[path] = f
	}
	if v := a2aClientViolations(files); len(v) > 0 {
		t.Fatalf("outbound A2A client in non-test sources:\n%s", strings.Join(v, "\n"))
	}
}

// TestA2AAgentIsReadOnly: the agent mirrors the read-only MCP charter (ADR
// 0006). Reads names exactly the two queries behind the two skills, the
// adapter names no use case, and cmd/a2a only builds the shared Service.
func TestA2AAgentIsReadOnly(t *testing.T) {
	adapter := parseDir(t, a2aAdapterDir)
	if got := readsMethods(adapter); !equalStrings(got, wantA2AReads) {
		t.Fatalf("Reads methods = %v, want exactly %v (widening the A2A surface is an ADR 0009 amendment)", got, wantA2AReads)
	}
	if v := adapterUseCaseViolations(adapter); len(v) > 0 {
		t.Fatalf("the adapter reaches the use cases directly:\n%s", strings.Join(v, "\n"))
	}
	if v := cmdUseCaseViolations(parseDir(t, a2aCmdDir)); len(v) > 0 {
		t.Fatalf("cmd/a2a names a use case:\n%s", strings.Join(v, "\n"))
	}
}

// TestA2ASensorsFailOnBadFixtures proves each sensor bites: a fixture that
// breaks the rule is flagged, a clean one is not.
func TestA2ASensorsFailOnBadFixtures(t *testing.T) {
	const prelude = "package x\n"

	client := parseSources(t, map[string]string{
		"bad.go":  prelude + "import _ \"" + a2aClientImp + "\"\n",
		"good.go": prelude + "import _ \"net/http\"\n",
	})
	if v := a2aClientViolations(client); len(v) != 1 || !strings.HasPrefix(v[0], "bad.go") {
		t.Errorf("client sensor = %v, want exactly bad.go", v)
	}

	uc := parseSources(t, map[string]string{
		"bad.go": prelude + "import \"m/" + usecasesImport + "\"\nvar _ = usecases.New\nvar _ = usecases.SubmitAcknowledgement\n",
		"ok.go":  prelude + "// usecases.SubmitAcknowledgement is only named in a comment\n",
	})
	v := adapterUseCaseViolations(uc)
	if len(v) != 3 {
		t.Errorf("adapter use-case sensor = %v, want the import plus two named identifiers", v)
	}
	for _, line := range v {
		if strings.HasPrefix(line, "ok.go") {
			t.Errorf("comment prose flagged: %s", line)
		}
	}

	cmd := parseSources(t, map[string]string{
		"main.go": prelude + "var _ = usecases.New\nvar _ = usecases.Deps{}\nvar _ = usecases.SuspendVendor\n",
	})
	if v := cmdUseCaseViolations(cmd); len(v) != 1 || !strings.Contains(v[0], "SuspendVendor") {
		t.Errorf("cmd sensor = %v, want only SuspendVendor", v)
	}

	reads := parseSources(t, map[string]string{
		"s.go": prelude + "type Reads interface {\n\tGetPurchaseOrder()\n\tGetScorecard()\n\tSubmitAcknowledgement()\n}\n",
	})
	if got := readsMethods(reads); equalStrings(got, wantA2AReads) {
		t.Errorf("Reads sensor accepted a widened interface: %v", got)
	}
}
