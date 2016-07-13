package parser

import (
	"fmt"
	"github.com/sonald/sc/lexer"
	"github.com/sonald/sc/util"
	"os"
	"reflect"
	"runtime"
	"strings"
)

//maximum lookaheads
const NR_LA = 4

type Parser struct {
	lex          *lexer.Scanner
	tokens       [NR_LA]lexer.Token // support 4-lookahead
	cursor       int
	eot          bool // meat EOT
	ctx          *AstContext
	currentScope *SymbolScope
	tu           *TranslationUnit
	verbose      bool
}

type ParseOption struct {
	filename    string
	dumpAst     bool
	dumpSymbols bool
	verbose     bool // log call trace
}

func NewParser() *Parser {
	p := &Parser{}

	var top = SymbolScope{}
	p.ctx = &AstContext{top: &top}
	p.currentScope = &top

	return p
}

func (self *Parser) peek(n int) lexer.Token {
	if n >= NR_LA || n < 0 {
		panic(fmt.Sprintf("only maximum of %d tokens lookahead supported", NR_LA))
	}

	tok := self.tokens[n]
	util.Printf("peek %s(%s)\n", lexer.TokKinds[tok.Kind], tok.AsString())
	return tok
}

func (self *Parser) getNextToken() lexer.Token {
	if self.eot {
		return lexer.Token{Kind: lexer.EOT}
	}

	tok := self.lex.Next()
	if tok.Kind == lexer.EOT {
		self.eot = true
	}
	return tok
}

func (self *Parser) next() lexer.Token {
	tok := self.tokens[0]
	for i := 1; i <= NR_LA-1; i++ {
		self.tokens[i-1] = self.tokens[i]
	}
	self.tokens[NR_LA-1] = self.getNextToken()
	//util.Printf("next %s(%s)\n", lexer.TokKinds[tok.Kind], tok.AsString())
	return tok
}

func (self *Parser) match(kd lexer.Kind) {
	if self.peek(0).Kind == kd {
		self.next()
	} else {
		self.parseError(self.peek(0),
			fmt.Sprintf("expect %s, but %s found",
				lexer.TokKinds[kd], lexer.TokKinds[self.peek(0).Kind]))
	}
}

// the only entry
func (self *Parser) Parse(opts *ParseOption) Ast {
	if f, err := os.Open(opts.filename); err != nil {
		util.Printf("%s\n", err.Error())
		return nil
	} else {
		self.lex = lexer.NewScanner(f)
	}

	for i := range self.tokens {
		self.tokens[i] = self.getNextToken()
	}

	self.verbose = opts.verbose

	return self.parseTU(opts)
}

// translation-unit: external-declaration+
func (self *Parser) parseTU(opts *ParseOption) Ast {
	self.tu = &TranslationUnit{}
	self.tu.filename = opts.filename
	for self.peek(0).Kind != lexer.EOT {
		self.parseExternalDecl(opts)
	}
	return self.tu
}

var storages map[string]Storage

var typeSpecifier map[string]bool

var typeQualifier map[string]Qualifier

func isStorageClass(tok lexer.Token) bool {
	_, ok := storages[tok.AsString()]
	return ok
}

func isTypeSpecifier(tok lexer.Token) bool {
	_, ok := typeSpecifier[tok.AsString()]
	return ok
}

func isTypeQualifier(tok lexer.Token) bool {
	_, ok := typeQualifier[tok.AsString()]
	return ok
}

func (self *Parser) parseError(tok lexer.Token, msg string) {
	panic(fmt.Sprintf("tok %s(%s), %s", lexer.TokKinds[tok.Kind], tok.AsString(), msg))
}

func (self *Parser) parseTypeDecl(opts *ParseOption, sym *Symbol) {
	defer self.trace("")()

	var ty SymbolType

	for {
		tok := self.peek(0)
		if tok.Kind == lexer.KEYWORD {
			self.next()
			if isStorageClass(tok) {
				if sym.Storage == NilStorage {
					sym.Storage = storages[tok.AsString()]
				} else {
					self.parseError(tok, "multiple storage class specified")
				}
			} else if isTypeSpecifier(tok) {
				//FIXME: handle multiple typespecifier
				if sym.Type != nil {
					if _, qualified := sym.Type.(*QualifiedType); !qualified {
						self.parseError(tok, "multiple type specifier")
					}
				}
				switch tok.AsString() {
				case "int":
					ty = &IntegerType{}
				case "float":
					ty = &FloatType{}
				default:
					self.parseError(tok, "not implemented")
				}

				if sym.Type == nil {
					sym.Type = ty
				} else {
					var qty = sym.Type.(*QualifiedType)
					for qty.Base != nil {
						qty = qty.Base.(*QualifiedType)
					}
					qty.Base = ty
				}

			} else if isTypeQualifier(tok) {
				sym.Type = &QualifiedType{Base: sym.Type, Qualifier: typeQualifier[tok.AsString()]}
				//self.parseError(tok, "multiple type qualifier specified")
			} else if tok.AsString() == "inline" {
				//ignore now
			} else {
				self.parseError(tok, "invalid declaration specifier")
			}
			//FIXME: handle usertype by typedef, struct, union
		} else {
			break
		}
	}

	util.Printf("parsed type template %v", sym)
}

func (self *Parser) parseFunctionParams(opts *ParseOption, decl *FunctionDecl) {
	defer self.trace("")()

	funcSym := self.currentScope.LookupSymbol(decl.Name)
	var ty = funcSym.Type.(*Function)
	for {
		if self.peek(0).Kind == lexer.RPAREN {
			break
		}

		var tmpl = &Symbol{}
		self.parseTypeDecl(opts, tmpl)
		if arg := self.parseDeclarator(opts, tmpl); arg == nil {
			break
		} else {
			switch arg.(type) {
			case *VariableDecl:
				var pd = &ParamDecl{decl.Node, arg.(*VariableDecl).Sym}
				decl.Args = append(decl.Args, pd)

				pty := decl.Scope.LookupSymbol(pd.Sym)
				ty.Args = append(ty.Args, pty.Type)
				util.Printf("parsed arg %v", pd.Repr())
			default:
				self.parseError(self.peek(0), "invalid parameter declaration")
			}
		}

		if self.peek(0).Kind == lexer.COMMA {
			self.next()
		}
	}
}

//FIXME: support full c99 declarator parsing
//FIXME: check redeclaration
func (self *Parser) parseDeclarator(opts *ParseOption, sym *Symbol) Ast {
	defer self.trace("")()

	var newSym = Symbol{Type: sym.Type, Storage: sym.Storage}
	self.AddSymbol(&newSym)

	var decl Ast

	tok := self.next()
	if tok.Kind == lexer.MUL {
		newSym.Type = &Pointer{newSym.Type}
		tok = self.next()
	}

	if tok.Kind != lexer.IDENTIFIER {
		self.parseError(tok, "expect identifier")
	}
	newSym.Name = tok

	switch self.peek(0).Kind {
	case lexer.OPEN_BRACKET: // array
		self.match(lexer.OPEN_BRACKET)
		if tok := self.next(); tok.Kind == lexer.INT_LITERAL {
			newSym.Type = &Array{newSym.Type, 1, []int{tok.AsInt()}}
		} else {
			self.parseError(tok, "invalid array type specifier")
		}
		self.match(lexer.CLOSE_BRACKET)

		decl = &VariableDecl{Sym: newSym.Name.AsString()}

	case lexer.LPAREN: // func
		self.match(lexer.LPAREN)
		var fdecl = &FunctionDecl{}
		decl = fdecl
		newSym.Type = &Function{Return: newSym.Type}
		fdecl.Name = newSym.Name.AsString()
		// when found definition of func, we need to chain fdecl.Scope with body
		fdecl.Scope = self.PushScope()
		self.parseFunctionParams(opts, fdecl)
		self.PopScope()
		self.match(lexer.RPAREN)

	default:
		decl = &VariableDecl{Sym: newSym.Name.AsString()}

		if self.peek(0).Kind == lexer.EQUAL {
			// parse initializer
			//decl.(&VariableDecl).init = init
		}
	}

	return decl
}

func (self *Parser) parseExternalDecl(opts *ParseOption) Ast {
	defer self.trace("")()

	var tmpl = &Symbol{}
	self.parseTypeDecl(opts, tmpl)
	for {
		if self.peek(0).Kind == lexer.SEMICOLON {
			self.next()
			break
		}

		if decl := self.parseDeclarator(opts, tmpl); decl == nil {
			break
		} else {
			switch decl.(type) {
			case *VariableDecl:
				self.tu.varDecls = append(self.tu.varDecls, decl.(*VariableDecl))
				util.Printf("parsed %v", decl.Repr())
			case *FunctionDecl:
				var fdecl = decl.(*FunctionDecl)
				self.tu.funcDecls = append(self.tu.funcDecls, fdecl)
				util.Printf("parsed %v", decl.Repr())

				if self.peek(0).Kind == lexer.LBRACE {
					if self.currentScope != fdecl.Scope.Parent {
						panic("fdecl should inside currentScope")
					}
					self.currentScope = fdecl.Scope
					fdecl.Body = self.parseCompoundStmt(opts)
					self.PopScope()

					// parse of function definition done
					goto done
				}

			default:
				self.parseError(self.peek(0), "")
			}
		}

		if self.peek(0).Kind == lexer.COMMA {
			self.next()
		}
	}

done:
	return nil
}

func (self *Parser) parseCompoundStmt(opts *ParseOption) *CompoundStmt {
	defer self.trace("")()
	var scope = self.PushScope()
	var compound = &CompoundStmt{Node: Node{self.ctx}, Scope: scope}

	self.match(lexer.LBRACE)

	for {
		if self.peek(0).Kind == lexer.RBRACE {
			break
		}
		compound.Stmts = append(compound.Stmts, self.parseStatement(opts))
	}
	self.match(lexer.RBRACE)
	self.PopScope()
	return compound
}

func (self *Parser) parseStatement(opts *ParseOption) Statement {
	defer self.trace("")()
	tok := self.peek(0)

	var stmt Statement
	// all normal statements

	// else
	switch tok.Kind {
	case lexer.KEYWORD:
		//FIXME: handle typedef usertype decl
		if isStorageClass(tok) || isTypeQualifier(tok) || isTypeSpecifier(tok) {
			stmt = self.parseDeclStatement(opts)
		} else {
			stmt = self.parseExprStatement(opts)
		}

	default:
		stmt = self.parseExprStatement(opts)
	}

	util.Printf("parsed %s\n", stmt.Repr())
	return stmt
}

func (self *Parser) parseDeclStatement(opts *ParseOption) *DeclStmt {
	defer self.trace("")()

	var declStmt = &DeclStmt{Node: Node{self.ctx}}

	var tmpl = &Symbol{}
	self.parseTypeDecl(opts, tmpl)
	for {
		if self.peek(0).Kind == lexer.SEMICOLON {
			self.next()
			break
		}

		if decl := self.parseDeclarator(opts, tmpl); decl == nil {
			break
		} else {
			switch decl.(type) {
			case *VariableDecl:
				declStmt.Decls = append(declStmt.Decls, decl.(*VariableDecl))
				util.Printf("parsed %v", decl.Repr())

			default:
				self.parseError(self.peek(0), "invalid declaration inside block")
			}
		}

		if self.peek(0).Kind == lexer.COMMA {
			self.next()
		}
	}

	return declStmt
}

func (self *Parser) parseExprStatement(opts *ParseOption) *ExprStmt {
	defer self.trace("")()
	var exprStmt = &ExprStmt{Node: Node{self.ctx}}
	expr := self.parseExpression(opts, 0)
	self.match(lexer.SEMICOLON)
	exprStmt.Expr = expr

	return exprStmt
}

type Associativity int
type Pred int
type Arity int

const (
	NoAssoc Associativity = 0 << iota
	RightAssoc
	LeftAssoc
)

const (
	NoneArity = 0
	Unary     = 1
	Binary    = 2
	Ternary   = 3
)

// one token may be used ad prefix or postfix/infix, so we need two Precedences for a token
type operation struct {
	lexer.Token
	Associativity
	NudPred int
	LedPred int
	nud     func(p *Parser, op *operation) Expression
	led     func(p *Parser, lhs Expression, op *operation) Expression
}

// operation templates
var operations map[lexer.Kind]*operation

// alloc new operation by copying specific template
func newOperation(tok lexer.Token) *operation {
	var op = *operations[tok.Kind]
	op.Token = tok
	return &op
}

// for binary op
//
func binop_led(p *Parser, lhs Expression, op *operation) Expression {
	defer p.trace("")()
	p.next() // eat op
	rhs := p.parseExpression(nil, op.LedPred)

	var expr = &BinaryOperation{Node{p.ctx}, op.Token.Kind, lhs, rhs}
	util.Printf("parsed %v", expr.Repr())
	return expr
}

// for unary (including prefix)
func unaryop_nud(p *Parser, op *operation) Expression {
	defer p.trace("")()
	p.next()
	// op.Pred is wrong, need prefix pred
	var expr = p.parseExpression(nil, op.NudPred)
	return &UnaryOperation{Node{p.ctx}, op.Kind, false, expr}
}

// for postfix
func unaryop_led(p *Parser, lhs Expression, op *operation) Expression {
	defer p.trace("")()
	return nil
}

// end of expr
func expr_led(p *Parser, lhs Expression, op *operation) Expression {
	return nil
}

// for ID
func id_nud(p *Parser, op *operation) Expression {
	defer p.trace("")()
	p.next()
	return &DeclRefExpr{Node{p.ctx}, op.Token.AsString()}
}

// for Literal (int, float, string, char...)
func literal_nud(p *Parser, op *operation) Expression {
	defer p.trace("")()
	p.next()
	switch op.Kind {
	case lexer.INT_LITERAL:
		return &IntLiteralExpr{Node: Node{p.ctx}, Tok: op.Token}
	case lexer.STR_LITERAL:
		return &StringLiteralExpr{Node: Node{p.ctx}, Tok: op.Token}
	case lexer.CHAR_LITERAL:
		return &CharLiteralExpr{Node: Node{p.ctx}, Tok: op.Token}
	}
	return nil
}

func (self *Parser) parseExpression(opts *ParseOption, rbp int) Expression {
	defer self.trace("")()

	if self.peek(0).Kind == lexer.SEMICOLON {
		return nil
	}

	operand := newOperation(self.peek(0))
	lhs := operand.nud(self, operand)

	op := newOperation(self.peek(0))
	for rbp < op.LedPred {
		lhs = op.led(self, lhs, op)
		op = newOperation(self.peek(0))
	}

	return lhs
}

func (self *Parser) PushScope() *SymbolScope {
	var scope = &SymbolScope{}
	scope.Parent = self.currentScope
	self.currentScope.Children = append(self.currentScope.Children, scope)

	self.currentScope = scope
	return scope
}

func (self *Parser) PopScope() *SymbolScope {
	if self.currentScope == self.ctx.top {
		panic("cannot pop top of the scope chain")
	}

	var ret = self.currentScope
	self.currentScope = ret.Parent
	return ret
}

func (self *Parser) AddSymbol(sym *Symbol) {
	self.currentScope.AddSymbol(sym)
}

func (self *Parser) LookupSymbol(name string) *Symbol {
	return self.currentScope.LookupSymbol(name)
}

// this is useless, need to trace symbol hierachy from TU
func (self *Parser) DumpSymbols() {
	var dumpSymbols func(scope *SymbolScope, level int)
	dumpSymbols = func(scope *SymbolScope, level int) {
		for _, sym := range scope.Symbols {
			fmt.Printf("%s%s\n", strings.Repeat(" ", level*2), sym.Name.AsString())
		}

		for _, sub := range scope.Children {
			dumpSymbols(sub, level+1)
		}
	}

	dumpSymbols(self.ctx.top, 0)
}

func (self *Parser) DumpAst() {
	var top Ast = self.tu
	var stack int = 0
	var scope *SymbolScope

	fmt.Println("DumpAst")
	var visit func(Ast)
	var log = func(msg string) {
		fmt.Printf("%s%s\n", strings.Repeat("  ", stack), msg)
	}

	visit = func(ast Ast) {
		switch ast.(type) {
		case *TranslationUnit:
			tu := ast.(*TranslationUnit)
			scope = self.ctx.top
			for _, d := range tu.funcDecls {
				stack++
				visit(d)
				stack--
			}
			for _, d := range tu.varDecls {
				stack++
				visit(d)
				stack--
			}

		case *IntLiteralExpr:
			e := ast.(*IntLiteralExpr)
			log(e.Repr())

		case *CharLiteralExpr:
			e := ast.(*CharLiteralExpr)
			log(e.Repr())

		case *StringLiteralExpr:
			e := ast.(*StringLiteralExpr)
			log(e.Repr())

		case *BinaryOperation:
			var (
				e  = ast.(*BinaryOperation)
				ty = reflect.TypeOf(e).Elem()
			)
			log(fmt.Sprintf("%s(%s)", ty.Name(), lexer.TokKinds[e.Op]))
			stack++
			visit(e.LHS)
			visit(e.RHS)
			stack--

		case *DeclRefExpr:
			e := ast.(*DeclRefExpr)
			log(e.Repr())

		case *UnaryOperation:
			var (
				e  = ast.(*UnaryOperation)
				ty = reflect.TypeOf(e).Elem()
			)
			log(fmt.Sprintf("%s(%s)", ty.Name(), lexer.TokKinds[e.Op]))
			stack++
			visit(e.expr)
			stack--

		case *ConditionalOperation:
		case *ExprStmt:
			e := ast.(*ExprStmt)
			ty := reflect.TypeOf(e).Elem()
			log(fmt.Sprintf("%s", ty.Name()))
			stack++
			visit(e.Expr)
			stack--

		case *VariableDecl:
			e := ast.(*VariableDecl)
			sym := scope.LookupSymbol(e.Sym)

			log(fmt.Sprintf("VarDecl(%s)", sym))
			if e.init != nil {
				stack++
				visit(e.init)
				stack--
			}

		case *Initializer:
		case *ParamDecl:
			e := ast.(*ParamDecl)
			sym := scope.LookupSymbol(e.Sym)
			ty := reflect.TypeOf(e).Elem()
			log(fmt.Sprintf("%s(%v)", ty.Name(), sym))

		case *FunctionDecl:
			e := ast.(*FunctionDecl)
			scope = e.Scope
			sym := scope.LookupSymbol(e.Name)
			log(fmt.Sprintf("FuncDecl(%v)", sym))

			stack++
			for _, arg := range e.Args {
				visit(arg)
			}
			stack--

			if e.Body != nil {
				visit(e.Body)
			}

		case *LabelStmt:
		case *CaseStmt:
		case *DefaultStmt:
		case *ReturnStmt:
		case *IfStmt:
		case *SwitchStmt:
		case *WhileStmt:
		case *DoStmt:
		case *DeclStmt:
			e := ast.(*DeclStmt)
			log("DeclStmt")
			stack++
			for _, stmt := range e.Decls {
				visit(stmt)
			}
			stack--

		case *ForStmt:
		case *GotoStmt:
		case *ContinueStmt:
		case *BreakStmt:
		case *CompoundStmt:
			e := ast.(*CompoundStmt)
			scope = e.Scope
			log("CompoundStmt")
			stack++
			for _, stmt := range e.Stmts {
				visit(stmt)
			}
			stack--

		default:
			break
		}
	}

	visit(top)
}

func (self *Parser) trace(msg string) func() {
	var __func__ string
	if self.verbose {
		pc, _, _, _ := runtime.Caller(1)
		__func__ = runtime.FuncForPC(pc).Name()
		util.Printf(util.Parser, util.Debug, "Enter %s: %s\n", __func__, msg)
	}
	return func() {
		if self.verbose {
			util.Printf(util.Parser, util.Debug, "Exit %s: %s\n", __func__, msg)
		}
	}
}

func init() {
	util.Println(util.Parser, util.Debug, "init parser")

	storages = make(map[string]Storage)
	storages["auto"] = Auto
	storages["static"] = Static
	storages["external"] = External
	storages["register"] = Register
	storages["typedef"] = Typedef

	typeSpecifier = make(map[string]bool)
	var ts = [...]string{"void", "char", "short", "int", "long", "float",
		"double", "signed", "unsigned", "struct", "union", "enum"}
	for _, v := range ts {
		typeSpecifier[v] = true
	}

	typeQualifier = make(map[string]Qualifier)
	typeQualifier["const"] = Const
	typeQualifier["restrict"] = Restrict
	typeQualifier["volatile"] = Volatile

	operations = make(map[lexer.Kind]*operation)

	// make , right assoc, so evaluation begins from leftmost expr
	operations[lexer.COMMA] = &operation{lexer.Token{}, LeftAssoc, -1, 10, nil, binop_led}

	operations[lexer.ASSIGN] = &operation{lexer.Token{}, RightAssoc, -1, 20, nil, binop_led}
	operations[lexer.MUL_ASSIGN] = &operation{lexer.Token{}, RightAssoc, -1, 20, nil, binop_led}
	operations[lexer.DIV_ASSIGN] = &operation{lexer.Token{}, RightAssoc, -1, 20, nil, binop_led}
	operations[lexer.MOD_ASSIGN] = &operation{lexer.Token{}, RightAssoc, -1, 20, nil, binop_led}
	operations[lexer.PLUS_ASSIGN] = &operation{lexer.Token{}, RightAssoc, -1, 20, nil, binop_led}
	operations[lexer.MINUS_ASSIGN] = &operation{lexer.Token{}, RightAssoc, -1, 20, nil, binop_led}
	operations[lexer.LSHIFT_ASSIGN] = &operation{lexer.Token{}, RightAssoc, -1, 20, nil, binop_led}
	operations[lexer.RSHIFT_ASSIGN] = &operation{lexer.Token{}, RightAssoc, -1, 20, nil, binop_led}
	operations[lexer.AND_ASSIGN] = &operation{lexer.Token{}, RightAssoc, -1, 20, nil, binop_led}
	operations[lexer.OR_ASSIGN] = &operation{lexer.Token{}, RightAssoc, -1, 20, nil, binop_led}
	operations[lexer.XOR_ASSIGN] = &operation{lexer.Token{}, RightAssoc, -1, 20, nil, binop_led}

	//?:
	operations[lexer.QUEST] = &operation{lexer.Token{}, RightAssoc, -1, 30, nil, nil}

	operations[lexer.LOG_OR] = &operation{lexer.Token{}, LeftAssoc, -1, 40, nil, nil}
	operations[lexer.LOG_AND] = &operation{lexer.Token{}, LeftAssoc, -1, 50, nil, nil}

	operations[lexer.OR] = &operation{lexer.Token{}, LeftAssoc, -1, 60, nil, nil}
	operations[lexer.XOR] = &operation{lexer.Token{}, LeftAssoc, -1, 70, nil, nil}
	operations[lexer.AND] = &operation{lexer.Token{}, LeftAssoc, 150, 80, nil, nil}

	operations[lexer.EQUAL] = &operation{lexer.Token{}, LeftAssoc, -1, 90, nil, nil}
	operations[lexer.NE] = &operation{lexer.Token{}, LeftAssoc, -1, 90, nil, nil}

	// >, <, <=, >=
	operations[lexer.GREAT] = &operation{lexer.Token{}, LeftAssoc, -1, 100, nil, nil}
	operations[lexer.LESS] = &operation{lexer.Token{}, LeftAssoc, -1, 100, nil, nil}
	operations[lexer.GE] = &operation{lexer.Token{}, LeftAssoc, -1, 100, nil, nil}
	operations[lexer.LE] = &operation{lexer.Token{}, LeftAssoc, -1, 100, nil, nil}

	operations[lexer.LSHIFT] = &operation{lexer.Token{}, LeftAssoc, -1, 110, nil, nil}
	operations[lexer.RSHIFT] = &operation{lexer.Token{}, LeftAssoc, -1, 110, nil, nil}

	operations[lexer.MINUS] = &operation{lexer.Token{}, LeftAssoc, 150, 120, unaryop_nud, binop_led}
	operations[lexer.PLUS] = &operation{lexer.Token{}, LeftAssoc, 150, 120, unaryop_nud, binop_led}

	operations[lexer.MUL] = &operation{lexer.Token{}, LeftAssoc, 150, 130, unaryop_nud, binop_led}
	operations[lexer.DIV] = &operation{lexer.Token{}, LeftAssoc, -1, 130, nil, binop_led}
	operations[lexer.MOD] = &operation{lexer.Token{}, LeftAssoc, -1, 130, nil, binop_led}

	// cast
	// NOTE: ( can appear at a lot of places: primary (expr), postfix (type){initlist}, postif func()
	// need special take-care
	operations[lexer.LPAREN] = &operation{lexer.Token{}, LeftAssoc, -1, 140, nil, binop_led}

	// unary !, ~
	operations[lexer.NOT] = &operation{lexer.Token{}, LeftAssoc, -1, 150, nil, binop_led}
	operations[lexer.TILDE] = &operation{lexer.Token{}, LeftAssoc, -1, 150, nil, binop_led}
	// &, *, +, - is assigned beforehand

	// prefix and postfix
	operations[lexer.INC] = &operation{lexer.Token{}, LeftAssoc, 150, 160, unaryop_nud, unaryop_led}
	operations[lexer.DEC] = &operation{lexer.Token{}, LeftAssoc, 150, 160, unaryop_nud, unaryop_led}

	operations[lexer.OPEN_BRACKET] = &operation{lexer.Token{}, LeftAssoc, -1, 160, unaryop_nud, unaryop_led}
	operations[lexer.DOT] = &operation{lexer.Token{}, LeftAssoc, -1, 160, unaryop_nud, unaryop_led}
	operations[lexer.REFERENCE] = &operation{lexer.Token{}, LeftAssoc, -1, 160, unaryop_nud, unaryop_led}

	operations[lexer.INT_LITERAL] = &operation{lexer.Token{}, NoAssoc, 200, -1, literal_nud, nil}
	operations[lexer.STR_LITERAL] = &operation{lexer.Token{}, NoAssoc, 200, -1, literal_nud, nil}
	operations[lexer.IDENTIFIER] = &operation{lexer.Token{}, NoAssoc, 200, -1, id_nud, nil}

	operations[lexer.SEMICOLON] = &operation{lexer.Token{}, NoAssoc, -1, -1, nil, expr_led}
}
