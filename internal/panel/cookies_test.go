package panel

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// notHostOnly says what would let another name under the dashboard's domain
// set cookie c for the dashboard too, or "" when nothing would. A joined
// machine owns such a name (<machine id>.m.<domain>), so a compromised one
// could plant a cookie the dashboard reads (cookie tossing). Browsers take a
// __Host- cookie only from the name it's for, and only Secure, with Path=/
// and no Domain. HttpOnly keeps it from the page's scripts, and SameSite
// from other sites' requests.
func notHostOnly(c *http.Cookie) string {
	switch {
	case !strings.HasPrefix(c.Name, "__Host-"):
		return "its name doesn't start with __Host-"
	case !c.Secure:
		return "it isn't Secure"
	case c.Path != "/":
		return "its path is " + strconv.Quote(c.Path) + ", not /"
	case c.Domain != "":
		return "it names the domain " + c.Domain
	case !c.HttpOnly:
		return "it isn't HttpOnly"
	case c.SameSite != http.SameSiteStrictMode && c.SameSite != http.SameSiteLaxMode:
		return "it has no SameSite"
	}
	return ""
}

// checkHostOnly reports each cookie res sets that isn't host-only.
func checkHostOnly(t *testing.T, res *http.Response) {
	t.Helper()
	for _, c := range res.Cookies() {
		if why := notHostOnly(c); why != "" {
			t.Errorf("the dashboard sets the cookie %s, which another name under its domain could set too: %s (%s)", c.Name, why, c)
		}
	}
}

// Every cookie the dashboard sets is host-only (see notHostOnly): the
// session's, the second sign-in step's and Sign in with Whop's, as each is
// set and cleared. The panel, which answers every request to the
// dashboard's name, sets cookies only through setHostCookie, and always one
// with a __Host- name.
func TestEveryCookieTheDashboardSetsIsHostOnly(t *testing.T) {
	f, e, _, _, _ := sellingWithSignIn(t)
	for what, set := range map[string]func(http.ResponseWriter){
		"the session's":              func(w http.ResponseWriter) { e.srv.setSessionCookie(w, randomToken(32)) },
		"the session's, cleared":     clearSessionCookie,
		"the second step's":          func(w http.ResponseWriter) { setPendingCookie(w, randomToken(32)) },
		"the second step's, cleared": clearPendingCookie,
	} {
		rec := httptest.NewRecorder()
		set(rec)
		if res := rec.Result(); len(res.Cookies()) != 1 {
			t.Errorf("%s cookie: %q", what, res.Header.Values("Set-Cookie"))
		} else {
			checkHostOnly(t, res)
		}
	}
	b := newBrowser(t, e)
	res, authorize := b.visit(whopSignInPath)
	if len(res.Cookies()) != 1 {
		t.Fatalf("leaving for Whop sets %q", res.Header.Values("Set-Cookie"))
	}
	if res, _ = b.visit(f.approve(t, authorize, "user_alex")); len(res.Cookies()) != 2 {
		t.Fatalf("coming back from Whop sets %q", res.Header.Values("Set-Cookie"))
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	consts := map[string]string{}
	var names []*ast.CallExpr
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.CONST {
				for _, spec := range g.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if i < len(vs.Values) {
							if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
								consts[name.Name], _ = strconv.Unquote(lit.Value)
							}
						}
					}
				}
			}
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "setHostCookie" {
				continue
			}
			ast.Inspect(d, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SelectorExpr:
					if x, ok := n.X.(*ast.Ident); ok && x.Name == "http" && (n.Sel.Name == "SetCookie" || n.Sel.Name == "Cookie") {
						t.Errorf("%s: http.%s, where only setHostCookie sets cookies", fset.Position(n.Pos()), n.Sel.Name)
					}
				case *ast.BasicLit:
					if v, err := strconv.Unquote(n.Value); n.Kind == token.STRING && err == nil && strings.EqualFold(v, "Set-Cookie") {
						t.Errorf("%s: a Set-Cookie header, where only setHostCookie sets cookies", fset.Position(n.Pos()))
					}
				case *ast.CallExpr:
					if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "setHostCookie" {
						names = append(names, n)
					}
				}
				return true
			})
		}
	}
	if len(names) == 0 {
		t.Fatal("nothing calls setHostCookie")
	}
	for _, call := range names {
		var name string
		if len(call.Args) > 1 {
			switch a := call.Args[1].(type) {
			case *ast.Ident:
				name = consts[a.Name]
			case *ast.BasicLit:
				name, _ = strconv.Unquote(a.Value)
			}
		}
		if !strings.HasPrefix(name, "__Host-") {
			t.Errorf("%s: setHostCookie with the cookie name %q, not a __Host- one", fset.Position(call.Pos()), name)
		}
	}
}
