// Copyright 2017 The CCGO Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ccgo

import (
	"fmt"

	"github.com/cznic/ir"
	"github.com/cznic/sqlite2go/internal/c99"
)

func (g *gen) define(n *c99.Declarator) {
more:
	n = g.normalizeDeclarator(n)
	defined := true
	if n.Linkage == c99.LinkageExternal {
		_, defined = g.externs[n.Name()]
	}
	if _, ok := g.producedDeclarators[n]; defined && !ok {
		g.producedDeclarators[n] = struct{}{}
		g.tld(n)
	}

	for g.queue.Front() != nil {
		x := g.queue.Front()
		g.queue.Remove(x)
		switch y := x.Value.(type) {
		case *c99.Declarator:
			n = y
			goto more
		case *c99.NamedType:
			g.defineNamedType(y)
		case *c99.TaggedEnumType:
			g.defineTaggedEnumType(y)
		case *c99.TaggedStructType:
			g.defineTaggedStructType(y)
		default:
			todo("%T", y)
		}
	}
}

func (g *gen) defineNamedType(t *c99.NamedType) {
	if _, ok := g.producedNamedTypes[t.Name]; ok {
		return
	}

	g.producedNamedTypes[t.Name] = struct{}{}
	g.w("\ntype T%s = %s\n", dict.S(t.Name), g.typ(t.Type))
}

func (g *gen) defineTaggedEnumType(t *c99.TaggedEnumType) {
	if _, ok := g.producedEnumTags[t.Tag]; ok {
		return
	}

	g.producedEnumTags[t.Tag] = struct{}{}
	et := t.Type.(*c99.EnumType)
	tag := dict.S(t.Tag)
	g.w("\ntype E%s %s\n", tag, g.typ(et.Enums[0].Operand.Type))
	g.w("\nconst (")
	var iota int64
	for i, v := range et.Enums {
		val := v.Operand.Value.(*ir.Int64Value).Value
		if i == 0 {
			g.w("\nC%s E%s = iota", dict.S(v.Token.Val), tag)
			if val != 0 {
				g.w(" %+d", val)
			}
			iota = val + 1
			continue
		}

		g.w("\nC%s", dict.S(v.Token.Val))
		if val == iota {
			iota++
			continue
		}

		g.w(" = %d", val)
		iota = val + 1
	}
	g.w("\n)\n")

}

func (g *gen) defineTaggedStructType(t *c99.TaggedStructType) {
	if _, ok := g.producedStructTags[t.Tag]; ok {
		return
	}

	g.producedStructTags[t.Tag] = struct{}{}
	g.w("\ntype S%s %s\n", dict.S(t.Tag), g.typ(t.Type))
}

func (g *gen) tld(n *c99.Declarator) {
	g.w("\n\n// %s is defined at %v", g.mangleDeclarator(n), g.position(n))
	t := c99.UnderlyingType(n.Type)
	if t.Kind() == c99.Function {
		g.functionDefinition(n)
		return
	}

	if g.isZeroInitializer(n.Initializer) {
		if g.escaped(n) {
			g.w("\nvar %s = bss + %d", g.mangleDeclarator(n), g.allocBSS(n.Type))
			return
		}

		switch x := t.(type) {
		case
			*c99.PointerType,
			*c99.StructType:

			g.w("\nvar %s = bss + %d\n", g.mangleDeclarator(n), g.allocBSS(n.Type))
		case c99.TypeKind:
			if x.IsArithmeticType() {
				g.w("\nvar %s %s\n", g.mangleDeclarator(n), g.typ(n.Type))
				break
			}
			todo("%v: %v", g.position(n), x)
		default:
			todo("%v: %s %v %T", g.position(n), dict.S(n.Name()), n.Type, x)
		}
		return
	}

	if g.escaped(n) {
		g.escapedTLD(n)
		return
	}

	switch n.Initializer.Case {
	case c99.InitializerExpr: // Expr
		g.w("\nvar %s = ", g.mangleDeclarator(n))
		g.convert(n.Initializer.Expr, n.Type)
		g.w("\n")
	default:
		todo("", g.position0(n), n.Initializer.Case)
	}
}

func (g *gen) escapedTLD(n *c99.Declarator) {
	if g.isConstInitializer(n.Initializer) {
		g.w("\nvar %s = ds + %d\n", g.mangleDeclarator(n), g.allocDS(n.Type, n.Initializer))
		return
	}

	switch x := c99.UnderlyingType(n.Type).(type) {
	case *c99.ArrayType:
		if x.Item.Kind() == c99.Char && n.Initializer.Expr.Operand.Value != nil {
			g.w("\nvar %s = ts + %d\n", g.mangleDeclarator(n), g.allocString(int(n.Initializer.Expr.Operand.Value.(*ir.StringValue).StringID)))
			return
		}
	}

	g.w("\nvar %s = bss + %d // %v \n", g.mangleDeclarator(n), g.allocBSS(n.Type), n.Type)
	g.w("\n\nfunc init() { *(*%s)(unsafe.Pointer(%s)) = ", g.ptyp(n.Type, false), g.mangleDeclarator(n))
	g.literal(n.Type, n.Initializer)
	g.w("}")
}

func (g *gen) functionDefinition(n *c99.Declarator) {
	g.nextLabel = 1
	g.w("\nfunc %s(tls *%sTLS", g.mangleDeclarator(n), crt)
	names := n.ParameterNames()
	t := n.Type.(*c99.FunctionType)
	if len(names) != len(t.Params) {
		if len(names) != 0 {
			if !(len(names) == 1 && names[0] == 0) {
				todo("", g.position(n), names, t.Params)
			}
		}

		names = make([]int, len(t.Params))
	}
	params := n.Parameters
	var escParams []*c99.Declarator
	switch {
	case len(t.Params) == 1 && t.Params[0].Kind() == c99.Void:
		// nop
	default:
		for i, v := range t.Params {
			var param *c99.Declarator
			if i < len(params) {
				param = params[i]
			}
			nm := names[i]
			g.w(", ")
			switch {
			case param != nil && param.AddressTaken:
				g.w("a%s %s", dict.S(nm), g.typ(v))
				escParams = append(escParams, param)
			default:
				if x, ok := c99.UnderlyingType(v).(*c99.PointerType); ok && x.Item.Kind() == c99.Function {
					v = x.Item
				}
				g.w("%s %s", mangleIdent(nm, false), g.typ(v))
				if isVaList(v) {
					continue
				}

				if v.Kind() == c99.Ptr {
					g.w(" /* %s */", g.ptyp(v, false))
				}
			}
		}
		if t.Variadic {
			g.w(", %s...interface{}", ap)
		}
	}
	g.w(")")
	void := t.Result.Kind() == c99.Void
	if !void {
		g.w("(r %s)", g.typ(t.Result))
	}
	g.functionBody(n.FunctionDefinition.FunctionBody, n.FunctionDefinition.LocalVariables(), void, escParams)
	g.w("\n")
}

func (g *gen) functionBody(n *c99.FunctionBody, vars []*c99.Declarator, void bool, escParams []*c99.Declarator) {
	if vars == nil {
		vars = []*c99.Declarator{}
	}
	f := false
	g.compoundStmt(n.CompoundStmt, vars, nil, !void, nil, nil, escParams, &f)
}

func (g *gen) mangleDeclarator(n *c99.Declarator) string {
	nm := n.Name()
	if num, ok := g.nums[n]; ok {
		return fmt.Sprintf("_%d%s", num, dict.S(nm))
	}

	if n.IsField {
		return mangleIdent(nm, true)
	}

	if n.Linkage == c99.LinkageExternal {
		switch {
		case g.externs[n.Name()] == nil:
			return crt + mangleIdent(nm, true)
		default:
			return mangleIdent(nm, true)
		}
	}

	return mangleIdent(nm, false)
}

func (g *gen) normalizeDeclarator(n *c99.Declarator) *c99.Declarator {
	if n == nil {
		return nil
	}

	switch n.Linkage {
	case c99.LinkageExternal:
		if d, ok := g.externs[n.Name()]; ok {
			return d
		}
	}

	return n
}

func (g *gen) declaration(n *c99.Declaration) {
	// DeclarationSpecifiers InitDeclaratorListOpt ';'
	g.initDeclaratorListOpt(n.InitDeclaratorListOpt)
}

func (g *gen) initDeclaratorListOpt(n *c99.InitDeclaratorListOpt) {
	if n == nil {
		return
	}

	g.initDeclaratorList(n.InitDeclaratorList)
}

func (g *gen) initDeclaratorList(n *c99.InitDeclaratorList) {
	for ; n != nil; n = n.InitDeclaratorList {
		g.initDeclarator(n.InitDeclarator)
	}
}

func (g *gen) initDeclarator(n *c99.InitDeclarator) {
	d := n.Declarator
	if d.DeclarationSpecifier.IsStatic() {
		return
	}

	if d.Referenced == 0 && d.Initializer == nil {
		return
	}

	if n.Case == c99.InitDeclaratorInit { // Declarator '=' Initializer
		g.initializer(d)
	}
}
