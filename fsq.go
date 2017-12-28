package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"

	"github.com/chzyer/readline"

	"fs"
)

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

func binaryop(op token.Token, l, r int64) int64 {
	switch op {
	case token.ADD:
		return l + r
	case token.SUB:
		return l - r
	case token.MUL:
		return l * r
	case token.QUO:
		return l / r
	case token.REM:
		return l % r
	default:
		panic(fmt.Errorf("operation %s not supported", op))
	}
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
	return terp
}

func (terp *interpreter) Global(label string, value interface{}) {
	if strings.ContainsRune(label, '.') {
		panic(fmt.Errorf("labels can not contain '.'"))
	}
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
			err = fmt.Errorf("error: multiple assignment not supported")
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
		index := terp.eval(exp.Index)
		for index.Kind() == reflect.Ptr {
			index = reflect.Indirect(index)
		}

		return recvr.Index(int(index.Int())).Addr()

	case *ast.SliceExpr:
		recvr := terp.eval(exp.X)
		for recvr.Kind() == reflect.Ptr {
			recvr = reflect.Indirect(recvr)
		}

		low := terp.eval(exp.Low)
		for low.Kind() == reflect.Ptr {
			low = reflect.Indirect(low)
		}
		if !low.IsValid() {
			low = reflect.ValueOf(int(0))
		}

		high := terp.eval(exp.High)
		for high.Kind() == reflect.Ptr {
			high = reflect.Indirect(high)
		}
		if !high.IsValid() {
			high = reflect.ValueOf(recvr.Len())
		}
		return recvr.Slice(int(low.Int()), int(high.Int()))

	case *ast.BasicLit:
		if exp.Kind != token.INT {
			panic(fmt.Errorf("only int literals are suported"))
		}
		i, err := strconv.Atoi(exp.Value)
		if err != nil {
			panic(fmt.Errorf("error parsing int literal %q", exp.Value))
		}
		return reflect.ValueOf(i)

	case *ast.BinaryExpr:
		l := terp.eval(exp.X)
		r := terp.eval(exp.Y)
		return reflect.ValueOf(binaryop(exp.Op, l.Int(), r.Int()))

	case *ast.CallExpr:
		f := terp.eval(exp.Fun)

		if f.Kind() != reflect.Func {
			panic(fmt.Errorf("%s not a function or method", expfmt(exp.Fun)))
		}

		if f.Type().NumIn() != len(exp.Args) {
			panic(fmt.Errorf("%q expects %d arguments", exp.Fun, f.Type().NumIn()))
		}

		in := make([]reflect.Value, len(exp.Args))

		for i := range in {
			in[i] = terp.eval(exp.Args[i])
		}

		out := f.Call(in)
		// TODO: how to handle multiple returns?
		return out[0]

	default:
		panic(fmt.Errorf("unknown type: %s", reflect.TypeOf(exp)))
	}
}

func cstr(i interface{}) string {
	v := reflect.ValueOf(i)
	for v.Kind() == reflect.Ptr {
		v = reflect.Indirect(v)
	}

	switch v.Kind() {
	case reflect.Array, reflect.Slice:
		if v.Type().Elem().Kind() != reflect.Uint8 {
			panic("argument to \"str\" is not a string")
		}
		slice := v.Slice(0, v.Len()).Bytes()
		i := 0
		// trim off zero bytes
		for slice[i] != 0 && i < len(slice) {
			i++
		}
		return string(slice[0:i])
	case reflect.String:
		s := v.String()
		i := 0
		for s[i] != 0 && i < len(s) {
			i++
		}
		return s[:i]
	}
	panic("argument to \"str\" is not a string")
}

func main() {
	fsshm, err := fs.NewFieldSystem()
	if err != nil {
		panic(err)
	}

	rl, err := readline.New("> ")
	if err != nil {
		panic(err)
	}
	defer rl.Close()

	terp := NewInterpreter()
	terp.Global("fs", fsshm)
	terp.Global("str", cstr)
	terp.Tag = "json"

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")

	for {
		line, err := rl.Readline()
		if err != nil { // io.EOF
			break
		}
		for _, line = range strings.Split(line, ";") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			value, err := terp.Eval(line)
			if err != nil {
				fmt.Println(err)
				continue
			}
			if !value.IsValid() {
				continue
			}

			err = encoder.Encode(value.Interface())
			if err != nil {
				fmt.Println(err)
			}
		}
	}
}
