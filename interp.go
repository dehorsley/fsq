package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/constant"
	"go/format"
	"go/parser"
	"go/token"
	"reflect"
	"runtime"
	"strings"
)

// TODO: promote all native values to constant.Value and demoted to concrete types when used in calls

// Format ast.Exp to Go code (poor-man's gofmt)
func expfmt(node interface{}) string {
	fset := token.NewFileSet()
	var buf bytes.Buffer
	err := format.Node(&buf, fset, node)
	if err != nil {
		panic(err)
	}
	return buf.String()
}

func fieldByTagName(v reflect.Value, tag, name string) reflect.Value {
	if v.Kind() != reflect.Struct {
		panic("fieldByTagName called on non-struct value")
	}

	for i := 0; i < v.NumField(); i++ {
		field := v.Type().Field(i)
		s, ok := field.Tag.Lookup(tag)
		if !ok {
			continue
		}
		if idx := strings.Index(s, ","); idx != -1 {
			s = s[:idx]
		}
		if s == name {
			return v.Field(i)
		}
	}
	return reflect.Value{}
}

type interpreter struct {
	globals map[string]reflect.Value
	Tag     string
}

func NewInterpreter() *interpreter {
	terp := &interpreter{
		globals: make(map[string]reflect.Value),
	}
	// Useful builtin functions, that can interact with the interpreter
	terp.globals["ls"] = reflect.ValueOf(func(ins ...interface{}) []string {
		children := make([]string, 0)
		if len(ins) == 0 {
			ins = append(ins, terp.globals)
		}

		for _, in := range ins {
			v := reflect.ValueOf(in)

			for i := 0; i < v.NumMethod(); i++ {
				m := v.Type().Method(i)
				children = append(children, m.Name)
			}

			for v.Kind() == reflect.Ptr {
				v = reflect.Indirect(v)
			}

			switch v.Kind() {
			case reflect.Struct:

				for i := 0; i < v.NumField(); i++ {
					field := v.Type().Field(i)
					name := field.Name
					if terp.Tag != "" {
						s, ok := field.Tag.Lookup(terp.Tag)
						if !ok {
							continue
						}
						name = s
						if idx := strings.Index(s, ","); idx != -1 {
							name = s[:idx]
						}
					}
					children = append(children, name)
				}
			case reflect.Map:
				keys := v.MapKeys()
				for _, key := range keys {
					children = append(children, fmt.Sprintf("%s", key))
				}
			}
		}
		return children
	})

	return terp
}

func (terp *interpreter) Global(label string, value interface{}) {
	v := reflect.ValueOf(value)
	terp.globals[strings.TrimSpace(label)] = v
}

func (terp *interpreter) Eval(line string) (value reflect.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			switch r := r.(type) {
			case runtime.Error:
				panic(r)
			case error:
				err = r
			default:
				err = fmt.Errorf("%s", r)
			}
		}
	}()

	var exp ast.Expr
	line = strings.TrimSpace(line)

	// TODO: not sure if this is really the right behavour
	if line == "" {
		return reflect.ValueOf(terp.globals), nil
	}

	if strings.ContainsRune(line, '=') {
		fields := strings.Split(line, "=")
		if len(fields) > 2 {
			err = fmt.Errorf("multiple assignment not supported")
			return
		}

		label := strings.TrimSpace(fields[0])
		exp, err = parser.ParseExpr(fields[1])
		if err != nil {
			return
		}
		terp.globals[label] = terp.eval(exp)
		return reflect.Value{}, nil
	}

	if v, ok := terp.globals[line]; ok {
		return v, nil
	}

	exp, err = parser.ParseExpr(line)
	if err != nil {
		return reflect.Value{}, err
	}
	value = terp.eval(exp)
	return value, err
}

// This guy does the actual work
func (terp *interpreter) eval(exp ast.Expr) reflect.Value {
	if exp == nil {
		return reflect.Value{}
	}
	switch exp := exp.(type) {
	case *ast.Ident:
		if v, ok := terp.globals[exp.String()]; ok {
			return v
		}
		panic(fmt.Errorf("unknown field or label %q", exp.String()))

	case *ast.SelectorExpr:
		recvr := terp.eval(exp.X)
		s := exp.Sel.String()

		f := recvr.MethodByName(s)
		if f.IsValid() {
			return f
		}

		for recvr.Kind() == reflect.Ptr {
			recvr = reflect.Indirect(recvr)
		}

		if recvr.Kind() != reflect.Struct {
			panic(fmt.Errorf("select field %q from type %q", s, recvr.Kind()))
		}

		if terp.Tag != "" {
			f = fieldByTagName(recvr, terp.Tag, s)
			if f.IsValid() {
				return f.Addr()
			}
		}

		f = recvr.FieldByName(s)
		if f.IsValid() {
			return f.Addr()
		}

		panic(fmt.Errorf("%q has no field %q", exp.X, s))

	case *ast.IndexExpr:
		recvr := terp.eval(exp.X)
		for recvr.Kind() == reflect.Ptr {
			recvr = reflect.Indirect(recvr)
		}

		return recvr.Index(index(terp.eval(exp.Index))).Addr()

	case *ast.SliceExpr:
		recvr := terp.eval(exp.X)
		for recvr.Kind() == reflect.Ptr {
			recvr = reflect.Indirect(recvr)
		}

		low := 0
		if v := terp.eval(exp.Low); v.IsValid() {
			low = index(v)
		}

		high := recvr.Len()
		if v := terp.eval(exp.High); v.IsValid() {
			high = index(v)
		}
		return recvr.Slice(low, high)

	case *ast.BasicLit:
		if exp.Kind != token.INT && exp.Kind != token.FLOAT &&
			exp.Kind != token.IMAG && exp.Kind != token.STRING &&
			exp.Kind != token.CHAR {
			panic(fmt.Errorf("unsupported literal of type %q", exp.Kind))
		}

		con := constant.MakeFromLiteral(exp.Value, exp.Kind, 0)
		if con.Kind() == constant.Unknown {
			panic(fmt.Errorf("could not parse literal %q", exp.Value))
		}

		return reflect.ValueOf(con)

	case *ast.BinaryExpr:
		x := constPromote(terp.eval(exp.X))
		y := constPromote(terp.eval(exp.Y))

		return reflect.ValueOf(constant.BinaryOp(x, exp.Op, y))

	case *ast.UnaryExpr:
		x := constPromote(terp.eval(exp.X))
		return reflect.ValueOf(constant.UnaryOp(exp.Op, x, 0))

	case *ast.CallExpr:
		f := terp.eval(exp.Fun)

		if f.Kind() != reflect.Func {
			panic(fmt.Errorf("%s not a function or method", expfmt(exp.Fun)))
		}

		in := make([]reflect.Value, len(exp.Args))

		for i := range exp.Args {
			in[i] = constDemote(terp.eval(exp.Args[i]))
		}

		out := f.Call(in)

		if len(out) == 0 {
			return reflect.Value{}
		}
		if len(out) == 1 {
			return out[0]
		}

		outiface := make([]interface{}, len(out))
		for i, ov := range out {
			outiface[i] = ov.Interface()
		}
		return reflect.ValueOf(outiface)

	case *ast.ParenExpr:
		return terp.eval(exp.X)

	default:
		panic(fmt.Errorf("unknown type: %s", reflect.TypeOf(exp)))
	}
}

func isInt(v reflect.Value) bool {
	return v.Kind() >= reflect.Int && v.Kind() <= reflect.Uint64
}

func isFloat(v reflect.Value) bool {
	return v.Kind() >= reflect.Float32 && v.Kind() <= reflect.Float64
}

func isComplex(v reflect.Value) bool {
	return v.Kind() >= reflect.Complex64 && v.Kind() <= reflect.Complex128
}

func index(v reflect.Value) int {
	if !v.IsValid() {
		panic("index called with empty value")
	}

	for v.Kind() == reflect.Ptr {
		v = reflect.Indirect(v)
	}
	if isInt(v) {
		return int(v.Int())
	}

	if c, ok := v.Interface().(constant.Value); ok {
		if c.Kind() != constant.Int {
			panic("index called with non int")
		}
		i, exact := constant.Int64Val(c)
		if !exact {
			panic("value cannot be represented as an index")
		}
		return int(i)
	}

	panic("index called with bad value")
}

func constDemote(v reflect.Value) reflect.Value {
	cv, ok := v.Interface().(constant.Value)
	if !ok {
		return v
	}
	switch cv.Kind() {
	case constant.Bool:
		return reflect.ValueOf(constant.BoolVal(cv))
	case constant.String:
		return reflect.ValueOf(constant.StringVal(cv))
	case constant.Int:
		i, _ := constant.Int64Val(cv)
		return reflect.ValueOf(i)
	case constant.Float:
		f, _ := constant.Float64Val(cv)
		return reflect.ValueOf(f)
	case constant.Complex:
		panic("complex numbers not supported yet")
	default:
		panic("cannot demote unknown constant")
	}
}

func constPromote(v reflect.Value) constant.Value {
	for v.Kind() == reflect.Ptr {
		v = reflect.Indirect(v)
	}
	vc, ok := v.Interface().(constant.Value)
	if ok {
		return vc
	}
	switch {
	case isInt(v):
		return constant.MakeInt64(v.Int())
	case isFloat(v):
		return constant.MakeFloat64(v.Float())
	case isComplex(v):
		// TODO
		panic("promoting complex numbers not implemented yet")
	default:
		panic(fmt.Errorf("unsuported promotion of type %q", v.Kind()))
	}
}
