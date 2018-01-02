package main

import (
	"encoding/json"
	"fmt"
	"go/constant"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/peterh/liner"

	"fs"
)

var history_path = filepath.Join(os.TempDir(), "fsqhistory")

func cstr(in interface{}) string {
	v := reflect.ValueOf(in)
	for v.Kind() == reflect.Ptr {
		v = reflect.Indirect(v)
	}

	var s string
	switch v.Kind() {
	case reflect.Array, reflect.Slice:
		if v.Type().Elem().Kind() != reflect.Uint8 {
			panic("argument to \"str\" is not a string")
		}
		s = string(v.Slice(0, v.Len()).Bytes())
	case reflect.String:
		s = v.String()
	default:
		panic("argument to \"str\" is not a string")
	}
	i := 0
	for i < len(s) && s[i] != 0 {
		i++
	}
	return s[:i]
}

func main() {
	fsshm, err := fs.NewFieldSystem()
	if err != nil {
		panic(err)
	}

	terp := NewInterpreter()
	terp.Tag = "json"
	terp.Global("fs", fsshm)
	terp.Global("str", cstr)

	lr := liner.NewLiner()
	defer lr.Close()

	lr.SetCompleter(func(line string) (c []string) {
		i := len(line)
		if i > 0 {
			i--
		}

		for i > 0 && line[i] != '.' && line[i] != '(' && line[i] != '[' {
			i--
		}

		expression := ""
		prefix := line[:i]
		search := line[i:]
		if i > 0 {
			// Trim off '.' '(' or '['
			prefix = line[:i+1]
			search = search[1:]

			j := i - 1
			nper := 0
			nsqb := 0
			for j > 0 && line[j] != '=' {
				switch line[j] {
				case '(':
					nper--
				case ')':
					nper++
				case '[':
					nsqb--
				case ']':
					nsqb++
				}
				if nper < 0 || nsqb < 0 {
					break
				}

				j--
			}

			if i != j {
				expression = line[j:i]
				if j != 0 {
					// Trim off '=' '(' or '['
					expression = expression[1:]
				}
			}
		}

		value, err := terp.Eval(fmt.Sprintf("ls(%s)", expression))
		if err != nil {
			return
		}

		names, ok := value.Interface().([]string)
		if !ok {
			return
		}

		for _, n := range names {
			if strings.HasPrefix(n, search) {
				c = append(c, prefix+n)
			}
		}
		return
	})

	encoder := json.NewEncoder(os.Stdout)
	// encoder.SetIndent("", " ")

	for {
		line, err := lr.Prompt("> ")
		if err != nil { // io.EOF
			break
		}
		lr.AppendHistory(line)
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

			switch value.Kind() {
			case reflect.Func:
				if value.Type().NumIn() == 0 {
					out := value.Call([]reflect.Value{})
					if len(out) > 0 {
						value = out[0]
					}
				}
				fmt.Println(value.Type())
				continue
			case reflect.Struct:
				if cv, ok := value.Interface().(constant.Value); ok {
					fmt.Println(cv)
					continue
				}
			}

			err = encoder.Encode(value.Interface())
			if err != nil {
				fmt.Println(err)
			}
		}
	}
}
