package ast

import (
	"testing"

	"mlc/internal/token"
)

func TestCloneIsDeep(t *testing.T) {
	orig := &FuncDecl{
		Name: "f",
		Body: &Block{Stmts: []Stmt{&ReturnStmt{Value: &Ident{Name: "x", P: token.Pos{Line: 2}}}}},
	}
	c := Clone(orig)
	ret := c.Body.Stmts[0].(*ReturnStmt)
	if ret == orig.Body.Stmts[0] || ret.Value == orig.Body.Stmts[0].(*ReturnStmt).Value {
		t.Fatal("the clone shares nodes with the original")
	}
	if ret.Value.(*Ident).Name != "x" || ret.Value.Pos().Line != 2 {
		t.Fatalf("the clone lost data: %+v", ret.Value)
	}
}
