package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/chzyer/readline"

	"fs"
)

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

	rl, err := readline.New("> ")
	if err != nil {
		panic(err)
	}
	defer rl.Close()

	terp := NewInterpreter()
	terp.Tag = "json"
	terp.Global("fs", fsshm)
	terp.Global("str", cstr)

	encoder := json.NewEncoder(os.Stdout)
	// encoder.SetIndent("", " ")

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

			if value.Kind() == reflect.Func && value.Type().NumIn() == 0 {
				out := value.Call([]reflect.Value{})
				if len(out) > 0 {
					value = out[0]
				}
			}

			err = encoder.Encode(value.Interface())
			if err != nil {
				fmt.Println(err)
			}
		}
	}
}
