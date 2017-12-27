package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"unicode"

	"github.com/chzyer/readline"

	"fs"
)

type interpreter struct {
	context reflect.Value
	globals map[string]reflect.Value
	funcs   map[string]func(reflect.Value) reflect.Value
}

func newInterpreter() *interpreter {
	return &interpreter{
		globals: make(map[string]interface{}),
		tag:     "json",
	}
}

func (terp *interpreter) SetContext(value reflect.Value) error {
	for value.Kind() == reflect.Ptr {
		value = reflect.Indirect(value)
	}
	switch value.Kind() {
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			sf := value.Type().Field(i)
			if sf.PkgPath != "" {
				// unexported field
				continue
			}
			t := value.Field(i)
			key := sf.Name
			if tag, ok := sf.Tag.Lookup(terp.tag); ok {
				key = tag
				if idx := strings.Index(tag, ","); idx != -1 {
					key = tag[:idx]
				}
			}
			terp.globals[key] = t.Addr().Interface()
		}
		return nil
	case reflect.Map:
		return fmt.Errorf("adding a map to globals supported yet")
	default:
		return fmt.Errorf("don't know what to do with %s", value.Kind())
	}
}

// Wrapper around eval that handles errors
func (terp *interpreter) Eval(s string) (value reflect.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			switch r := r.(type) {
			case runtime.Error:
				panic(r)
			case reflect.ValueError:
				panic(r)
			case string:
				err = fmt.Errorf("%s", strings.TrimPrefix(r, "reflect: "))
			case error:
				err = r
			default:
				err = fmt.Errorf("%s", r)
			}
		}
	}()

	var exp ast.Expr

	line := strings.TrimSpace(s)
	switch {
	case line == ".":
		return reflect.ValueOf(terp.globals), nil
	case strings.ContainsRune(line, '='):
		fields := strings.Split(line, "=")
		if len(fields) > 2 {
			err = fmt.Errorf("error: multiple assignment not supported")
			return value, err
		}

		lhs := strings.TrimSpace(fields[0])
		exp, err = parser.ParseExpr(fields[1])
		if err != nil {
			return
		}
		value = terp.eval(exp)
		terp.globals[lhs] = value
		return

	default:
		exp, err = parser.ParseExpr(line)
		if err != nil {
			return
		}
		value = terp.eval(exp)
		return value, err
	}
}

func (terp *interpreter) eval(exp ast.Expr) reflect.Value {
	if exp == nil {
		return reflect.Value{}
	}
	switch exp := exp.(type) {
	case *ast.Ident:
		v, ok := terp.globals[camel(exp.String())]
		if !ok {
			v, ok = terp.globals[exp.String()]
			if !ok {
				panic(fmt.Errorf("field \"%s\" not found", exp.String()))
			}
		}
		return reflect.ValueOf(v)

	case *ast.SelectorExpr:
		in := terp.eval(exp.X)
		s := exp.Sel.String()

		for in.Kind() == reflect.Ptr {
			in = reflect.Indirect(in)
		}

		if in.Kind() != reflect.Struct {
			panic(fmt.Errorf("select field \"%s\" from type %s", s, in.Kind()))
		}

		f := in.FieldByName(s)
		if f.IsValid() {
			return f.Addr()
		}

		f = in.FieldByName(camel(s))
		if f.IsValid() {
			return f.Addr()
		}

		panic(fmt.Errorf("\"%s\" has no field \"%s\"", exp.X, s))

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
			panic(fmt.Errorf("error parsing literal: %s", exp.Value))
		}
		return reflect.ValueOf(i)

	case *ast.BinaryExpr:
		l := terp.eval(exp.X)
		r := terp.eval(exp.Y)
		return reflect.ValueOf(binaryop(exp.Op, l.Int(), r.Int()))
	default:
		panic(fmt.Errorf("unknown type: %s\n", reflect.TypeOf(exp)))
	}
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

// Because of Go's export rules, fs fields are converted to camel case by this routine
func camel(s string) string {
	var b bytes.Buffer
	cap := true
	for i, r := range s {
		if r == '_' {
			if i == 0 {
				b.WriteString("X")
				cap = false
				continue
			}
			cap = true
			continue
		}
		if cap {
			b.WriteRune(unicode.ToUpper(r))
			cap = false
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func main() {
	fsshm, err := fs.NewFieldSystem()
	if err != nil {
		panic(err)
	}

	val := reflect.ValueOf(fsshm)
	for i := 0; i < val.NumMethod(); i++ {
		fmt.Println(val.Type().Method(i))
	}

	rl, err := readline.New("> ")
	if err != nil {
		panic(err)
	}
	defer rl.Close()

	terp := newInterpreter()

	err = terp.Globals(reflect.Indirect(reflect.Indirect(reflect.ValueOf(fsshm)).FieldByName("Fscom")).Addr())
	if err != nil {
		panic(err)
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")

	for {
		line, err := rl.Readline()
		if err != nil { // io.EOF
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		value, err := terp.Eval(line)
		if err != nil {
			fmt.Println(err)
			continue
		}

		err = encoder.Encode(value.Interface())
		if err != nil {
			fmt.Println(err)
		}
	}
}
