package injector

import (
	"go/format"
	"strings"
	"testing"
)

// gofmt formats src for comparison, failing the test if it does not parse.
func gofmt(t *testing.T, src string) string {
	t.Helper()
	out, err := format.Source([]byte(src))
	if err != nil {
		t.Fatalf("result is not valid Go: %v\n%s", err, src)
	}
	return string(out)
}

func dropLine(src, line string) string {
	return strings.Replace(src, line+"\n", "", 1)
}

const wireSrc = `package wire

type App struct {
	Env string
	// esb:inject:app-fields
}

func NewApp() (*App, error) {
	env := "dev"
	// esb:inject:app-services

	// esb:inject:app-init

	return &App{
		Env: env,
		// esb:inject:app-return-fields
	}, nil
}
`

var (
	appFields       = Target{Marker: "// esb:inject:app-fields", Find: StructFields("App")}
	appServices     = Target{Marker: "// esb:inject:app-services", Find: BeforeFirstCall("NewApp", "handler")}
	appInit         = Target{Marker: "// esb:inject:app-init", Find: BeforeReturn("NewApp")}
	appReturnFields = Target{Marker: "// esb:inject:app-return-fields", Find: CompositeLit("NewApp", "App")}
)

// With its marker in place, Inject must produce exactly what marker
// injection always did, so existing projects see no change.
func TestInject_MarkerInScopeMatchesMarkerInjection(t *testing.T) {
	for _, tc := range []struct {
		target Target
		code   string
	}{
		{appFields, "\tW *W"},
		{appServices, "\tsvc := New()"},
		{appInit, "\tw := NewW()"},
		{appReturnFields, "\t\tW: w,"},
	} {
		got, err := inject(wireSrc, tc.target, tc.code)
		if err != nil {
			t.Fatalf("inject(%s) error = %v", tc.target.Marker, err)
		}
		want, err := injectAfterMarker(wireSrc, tc.target.Marker, tc.code)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("inject(%s) differs from marker injection:\n%s\nwant:\n%s", tc.target.Marker, got, want)
		}
	}
}

// Without markers the code lands at each construct's natural place, and a
// handler added before its service still compiles: the service goes above
// the first handler constructor.
func TestInject_WithoutMarkers(t *testing.T) {
	src := wireSrc
	for _, m := range []string{"app-fields", "app-services", "app-init", "app-return-fields"} {
		src = dropLine(src, "\t// esb:inject:"+m)
		src = dropLine(src, "\t\t// esb:inject:"+m)
	}
	steps := []struct {
		target Target
		code   string
	}{
		{appFields, "\tH *handler.H"},
		{appInit, "\th := handler.NewH(svc)"},
		{appReturnFields, "\t\tH: h,"},
		{appServices, "\tsvc := service.New()"},
		{appInit, "\tw := projection.NewW()"},
	}
	for _, s := range steps {
		var err error
		if src, err = inject(src, s.target, s.code); err != nil {
			t.Fatalf("inject(%s) error = %v", s.target.Marker, err)
		}
	}
	want := `package wire

type App struct {
	Env string
	H   *handler.H
}

func NewApp() (*App, error) {
	env := "dev"

	svc := service.New()
	h := handler.NewH(svc)
	w := projection.NewW()
	return &App{
		Env: env,
		H:   h,
	}, nil
}
`
	if got := gofmt(t, src); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A marker moved out of its construct no longer decides placement.
func TestInject_MarkerOutsideScopeIsIgnored(t *testing.T) {
	src := strings.Replace(wireSrc, "\t// esb:inject:app-fields\n", "", 1)
	src = strings.Replace(src, "func NewApp", "// esb:inject:app-fields\nfunc NewApp", 1)
	got, err := inject(src, appFields, "\tW *W")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gofmt(t, got), "\tEnv string\n\tW   *W\n}") {
		t.Errorf("field not appended to App:\n%s", got)
	}
}

func TestInject_ListsOnOneLine(t *testing.T) {
	src := `package p

func NewDB() {
	db.AutoMigrate(&A{})
	db.AutoMigrate()
}

func main() {
	workers := []Worker{}
	var more = []Worker{a, b}
	_ = more
	run(workers)
}
`
	steps := []struct {
		target Target
		code   string
	}{
		{Target{Find: CallArgs("NewDB", "AutoMigrate")}, "\t\t&B{},"},
		{Target{Find: VarLit("main", "workers")}, "\t\tapp.W,"},
		{Target{Find: VarLit("main", "more")}, "c"},
	}
	for _, s := range steps {
		var err error
		if src, err = inject(src, s.target, s.code); err != nil {
			t.Fatalf("inject(%s) error = %v", s.target.Find.desc, err)
		}
	}
	got := gofmt(t, src)
	for _, want := range []string{
		"db.AutoMigrate(&A{}, &B{})\n\tdb.AutoMigrate()",
		"workers := []Worker{app.W}",
		"var more = []Worker{a, b, c}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestInject_SwitchCasesAndBlockEnd(t *testing.T) {
	src := `package p

func (a *A) Apply(name string) error {
	switch name {
	case "X":
		return nil
	default:
		return nil
	}
}

func (w *W) applyEvent() error {
	return tx(func() error {
		switch name {
		}
		return nil
	})
}

func RegisterRoutes() {
	health()
}
`
	steps := []struct {
		target Target
		code   string
	}{
		{Target{Find: SwitchCases("Apply")}, "\tcase \"Y\":\n\t\treturn nil"},
		{Target{Find: SwitchCases("applyEvent")}, "\tcase \"Y\":"},
		{Target{Find: BlockEnd("RegisterRoutes")}, "\t// TODO: route"},
	}
	for _, s := range steps {
		var err error
		if src, err = inject(src, s.target, s.code); err != nil {
			t.Fatalf("inject(%s) error = %v", s.target.Find.desc, err)
		}
	}
	got := gofmt(t, src)
	for _, want := range []string{
		"case \"X\":\n\t\treturn nil\n\tcase \"Y\":\n\t\treturn nil\n\tdefault:",
		"switch name {\n\t\tcase \"Y\":\n\t\t}",
		"health()\n\t// TODO: route\n}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestInject_ConstructMissing(t *testing.T) {
	src := "package p\n\nvar x = []int{\n\t// esb:inject:x\n}\n"
	got, err := inject(src, Target{Marker: "// esb:inject:x", Find: StructFields("App")}, "\t1,")
	if err != nil {
		t.Fatalf("marker fallback error = %v", err)
	}
	if !strings.Contains(got, "// esb:inject:x\n\t1,\n") {
		t.Errorf("code not placed after the marker:\n%s", got)
	}

	_, err = inject("package p\n", Target{Marker: "// esb:inject:x", Find: StructFields("App")}, "\t1,")
	if err == nil || !strings.Contains(err.Error(), "struct type App") || !strings.Contains(err.Error(), "esb:inject:x") {
		t.Errorf("error = %v, want one naming both the construct and the marker", err)
	}
}

// Marker text inside a string or a longer comment is not the marker.
func TestInject_MarkerMustBeWholeComment(t *testing.T) {
	src := "package p\n\nconst doc = \"// esb:inject:x\"\n\n// see // esb:inject:x\nvar v = 1\n"
	if _, err := inject(src, Target{Marker: "// esb:inject:x", Find: StructFields("App")}, "x"); err == nil {
		t.Fatal("expected an error: there is no real marker comment")
	}
}
